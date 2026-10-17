package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/TemirlanN-dev/cassette/internal/cassette"
	"github.com/TemirlanN-dev/cassette/internal/match"
	"github.com/TemirlanN-dev/cassette/internal/proxy"
)

// Flags shared by every command.
var (
	port         int
	target       string
	cassettePath string
	configPath   string
	realtime     bool
)

// exitCodeError makes cassette exit with a specific code, e.g. the test command's.
type exitCodeError struct{ code int }

func (e exitCodeError) Error() string { return fmt.Sprintf("exit code %d", e.code) }

func main() {
	root := &cobra.Command{
		Use:           "cassette",
		Short:         "Record and replay LLM API calls for fast, free, deterministic tests",
		SilenceUsage:  true,
		SilenceErrors: true, // errors are printed below, so a failing test command isn't reported twice
	}
	// Persistent flags are inherited by every subcommand.
	root.PersistentFlags().IntVar(&port, "port", 4000, "port to listen on (when running a command, a free port is picked unless you set this)")
	root.PersistentFlags().StringVar(&target, "target", "", "API to forward requests to, e.g. https://api.anthropic.com (required for record and auto)")
	root.PersistentFlags().StringVar(&cassettePath, "cassette", "cassettes/default.json", "cassette file to read and write")
	root.PersistentFlags().StringVar(&configPath, "config", "cassette.yaml", "matching rules file (optional)")

	root.AddCommand(
		newCommand(proxy.Record, "record", "Forward every request to the real API and save the answers to a fresh cassette"),
		newCommand(proxy.Auto, "auto", "Answer known requests from the cassette and record unknown ones"),
		newCommand(proxy.Replay, "replay", "Answer only from the cassette; fail on any request that wasn't recorded"),
	)

	err := root.Execute()
	var exit exitCodeError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		os.Exit(exit.code)
	default:
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func newCommand(mode proxy.Mode, name, short string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   name + " [-- command args...]",
		Short: short,
		Long: short + `.

With a command after --, cassette runs it with ANTHROPIC_BASE_URL and
OPENAI_BASE_URL pointing at itself, stops when it exits, and exits with
the command's exit code:

  cassette ` + name + ` --target https://api.anthropic.com -- npm test

Other SDKs need their base URL set to the address cassette prints.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && cmd.ArgsLenAtDash() != 0 {
				return fmt.Errorf("put the command to run after --, e.g. cassette %s -- npm test", name)
			}
			return run(mode, args, cmd.Flags().Changed("port"))
		},
	}
	if mode != proxy.Record {
		cmd.Flags().BoolVar(&realtime, "realtime", false, "replay streamed responses with their original timing instead of instantly")
	}
	return cmd
}

// run starts cassette in the given mode. If command is not empty, it runs the
// command against cassette and stops when it exits; otherwise it runs until Ctrl+C.
func run(mode proxy.Mode, command []string, portChosen bool) error {
	cfg, err := match.LoadConfig(configPath)
	if err != nil {
		return err
	}
	matcher, err := match.New(cfg)
	if err != nil {
		return fmt.Errorf("%s: %w", configPath, err)
	}

	var targetURL *url.URL
	if mode != proxy.Replay {
		if targetURL, err = parseTarget(target); err != nil {
			return err
		}
	}

	var store *cassette.Store
	switch mode {
	case proxy.Record:
		// Start fresh: replace the old recording right away.
		store = cassette.NewStore(cassettePath)
		if err := store.Save(); err != nil {
			return err
		}
	case proxy.Auto:
		store, err = cassette.OpenStore(cassettePath, false)
	case proxy.Replay:
		store, err = cassette.OpenStore(cassettePath, true)
	}
	if err != nil {
		return err
	}

	p := proxy.New(proxy.Options{Mode: mode, Target: targetURL, Store: store, Matcher: matcher, Realtime: realtime})

	switch mode {
	case proxy.Record:
		log.Printf("recording into %s, forwarding to %s", store.Path(), targetURL)
	case proxy.Auto:
		log.Printf("auto: %d recordings in %s, recording new requests from %s", p.Loaded(), store.Path(), targetURL)
	case proxy.Replay:
		log.Printf("replaying %d recordings from %s; unknown requests will fail", p.Loaded(), store.Path())
	}

	// When running a command, cassette sets the base URL for it, so any free
	// port works. Port 0 asks the operating system to pick one.
	listenPort := port
	if len(command) > 0 && !portChosen {
		listenPort = 0
	}
	// 127.0.0.1 means only this machine can connect; the proxy forwards API keys.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", listenPort))
	if err != nil {
		return err // e.g. the port is taken; the command never runs
	}
	srv := &http.Server{Handler: p}
	go srv.Serve(ln)
	base := "http://" + ln.Addr().String()
	log.Printf("listening on %s", base)

	var cmdErr error
	if len(command) > 0 {
		cmdErr = runCommand(command, base)
	} else {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		log.Printf("press Ctrl+C to stop")
		<-ctx.Done()
		stop()
	}

	// Let requests that are still running finish (and be saved) before exiting.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutdownCtx)

	if mode != proxy.Record {
		if n := p.Unused(); n > 0 {
			log.Printf("%d recordings were not used this run", n)
		}
	}
	if cmdErr != nil {
		return cmdErr
	}
	// An app may catch API errors and carry on, so tests can pass even though
	// the cassette is missing recordings. Fail the run so CI notices.
	if n := p.Misses(); n > 0 {
		log.Printf("%d requests had no recording in %s; run `cassette auto` to record them", n, store.Path())
		return exitCodeError{1}
	}
	return nil
}

// Environment variables the official SDKs read their base URL from.
var baseURLVars = []struct{ name, suffix string }{
	{"ANTHROPIC_BASE_URL", ""},
	{"OPENAI_BASE_URL", "/v1"}, // the OpenAI SDK expects /v1 to be part of the base URL
}

// runCommand runs the user's command with the SDKs pointed at cassette and
// returns an exitCodeError if the command fails.
func runCommand(command []string, base string) error {
	c := exec.Command(command[0], command[1:]...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	c.Env = commandEnv(os.Environ(), base)

	// Ctrl+C reaches the command directly from the terminal. Catch it here so
	// cassette doesn't quit first; it waits for the command and then shuts down.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)

	log.Printf("running: %s", strings.Join(command, " "))
	err := c.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		log.Printf("%s exited with code %d", command[0], exitErr.ExitCode())
		return exitCodeError{exitErr.ExitCode()}
	}
	if err != nil {
		return fmt.Errorf("running %s: %w", command[0], err)
	}
	return nil
}

// commandEnv returns environ with the SDK base-URL variables pointing at cassette,
// replacing any values that were already set.
func commandEnv(environ []string, base string) []string {
	env := make([]string, 0, len(environ)+len(baseURLVars))
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		replaced := false
		for _, v := range baseURLVars {
			// Windows variable names ignore case, so compare that way everywhere.
			if strings.EqualFold(name, v.name) {
				log.Printf("replacing %s for the command (was %s)", v.name, strings.TrimPrefix(kv, name+"="))
				replaced = true
			}
		}
		if !replaced {
			env = append(env, kv)
		}
	}
	for _, v := range baseURLVars {
		env = append(env, v.name+"="+base+v.suffix)
	}
	return env
}

func parseTarget(s string) (*url.URL, error) {
	if s == "" {
		return nil, fmt.Errorf("--target is required, e.g. --target https://api.anthropic.com")
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid --target %q, expected something like https://api.anthropic.com", s)
	}
	return u, nil
}
