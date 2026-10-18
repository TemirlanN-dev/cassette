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
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/TemirlanN-dev/cassette/internal/cassette"
	"github.com/TemirlanN-dev/cassette/internal/config"
	"github.com/TemirlanN-dev/cassette/internal/match"
	"github.com/TemirlanN-dev/cassette/internal/proxy"
	"github.com/TemirlanN-dev/cassette/internal/report"
)

// Flags shared by every command.
var (
	port         int
	target       string
	cassettePath string
	configPath   string
	envFlags     []string
	realtime     bool
)

// version is set when building a release: go build -ldflags "-X main.version=0.1.0".
var version = ""

// currentVersion returns the release version, or the module version for
// `go install ...@v0.1.0`, or "dev" for a local build.
func currentVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return strings.TrimPrefix(info.Main.Version, "v")
	}
	return "dev"
}

// exitCodeError makes cassette exit with a specific code, e.g. the test command's.
type exitCodeError struct{ code int }

func (e exitCodeError) Error() string { return fmt.Sprintf("exit code %d", e.code) }

func main() {
	root := &cobra.Command{
		Use:           "cassette",
		Short:         "Record and replay LLM API calls for fast, free, deterministic tests",
		Version:       currentVersion(),
		SilenceUsage:  true,
		SilenceErrors: true, // errors are printed below, so a failing test command isn't reported twice
	}
	// Persistent flags are inherited by every subcommand.
	root.PersistentFlags().IntVar(&port, "port", 4000, "port to listen on (when running a command, a free port is picked unless you set this)")
	root.PersistentFlags().StringVar(&target, "target", "", "API to forward requests to, e.g. https://api.anthropic.com (required for record and auto)")
	root.PersistentFlags().StringVar(&cassettePath, "cassette", "cassettes/default.jsonl", "cassette file to read and write")
	root.PersistentFlags().StringVar(&configPath, "config", "cassette.yaml", "settings file (optional)")
	root.PersistentFlags().StringArrayVar(&envFlags, "env", nil, "also point this environment variable at cassette for the command, e.g. --env MISTRAL_BASE_URL=/v1 (repeatable)")

	root.AddCommand(
		newCommand(proxy.Record, "record", "Forward every request to the real API and save the answers to a fresh cassette"),
		newCommand(proxy.Auto, "auto", "Answer known requests from the cassette and record unknown ones"),
		newCommand(proxy.Replay, "replay", "Answer only from the cassette; fail on any request that wasn't recorded"),
		newShowCommand(),
		newDiffCommand(),
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

For other libraries, name the environment variable they read their
address from, plus the path to keep:

  cassette ` + name + ` --target https://api.mistral.ai --env MISTRAL_BASE_URL=/v1 -- npm test`,
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

func newShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show FILE",
		Short: "Print a cassette in readable form",
		Long: `Print a cassette in readable form: each request, and the answer and
tool calls in its response.

Git can use this to show readable diffs of cassette files. Run once per repo:

  git config diff.cassette.textconv "cassette show"

and add this line to .gitattributes:

  cassettes/*.jsonl diff=cassette`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := cassette.Load(args[0])
			if err != nil {
				return err
			}
			report.Show(os.Stdout, c)
			return nil
		},
	}
}

func newDiffCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "diff [OLD NEW]",
		Short: "Show how the recordings changed: answers, tool calls and requests",
		Long: `Show how the recordings changed: answers, tool calls and requests.

With no arguments, compares the cassette with its last committed version in
Git. With two files, compares them. Exits with code 1 if anything changed.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 0 && len(args) != 2 {
				return fmt.Errorf("give no files (compare with Git) or two files (OLD NEW)")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDiff(args)
		},
	}
}

func runDiff(args []string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	matcher, err := match.New(cfg.Match)
	if err != nil {
		return fmt.Errorf("%s: %w", configPath, err)
	}

	var before, after *cassette.Cassette
	if len(args) == 2 {
		if before, err = cassette.Load(args[0]); err != nil {
			return err
		}
		if after, err = cassette.Load(args[1]); err != nil {
			return err
		}
	} else {
		if after, err = cassette.Load(cassettePath); err != nil {
			return err
		}
		if before, err = committedVersion(cassettePath); err != nil {
			return err
		}
	}

	res := report.Diff(os.Stdout, before.Interactions, after.Interactions, matcher)
	if res.Any() {
		return exitCodeError{1}
	}
	return nil
}

// committedVersion reads the version of path in the last Git commit. A file
// that was never committed counts as empty, so everything in it shows as new.
func committedVersion(path string) (*cassette.Cassette, error) {
	// Run Git from the file's own folder; "HEAD:./name" then means "name, in
	// this folder", so we never have to work out the path from the repo root.
	git := func(args ...string) ([]byte, error) {
		c := exec.Command("git", args...)
		c.Dir = filepath.Dir(path)
		return c.Output()
	}
	if _, err := git("rev-parse", "--git-dir"); err != nil {
		return nil, fmt.Errorf("not in a Git repository; to compare two files, run: cassette diff OLD NEW")
	}
	data, err := git("show", "HEAD:./"+filepath.Base(path))
	if err != nil {
		log.Printf("%s isn't in the last commit yet, so everything in it counts as new", path)
		return &cassette.Cassette{}, nil
	}
	return cassette.Parse(data)
}

// run starts cassette in the given mode. If command is not empty, it runs the
// command against cassette and stops when it exits; otherwise it runs until Ctrl+C.
func run(mode proxy.Mode, command []string, portChosen bool) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	matcher, err := match.New(cfg.Match)
	if err != nil {
		return fmt.Errorf("%s: %w", configPath, err)
	}
	vars, err := envVars(cfg.Env, envFlags)
	if err != nil {
		return err
	}
	if len(command) == 0 && len(vars) > len(defaultEnvVars) {
		log.Printf("note: env settings only apply when cassette runs a command after --")
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
	if old := store.MigratedFrom(); old != "" {
		if err := store.Save(); err != nil {
			return err
		}
		log.Printf("converted %s to the new format in %s; you can delete %s", old, store.Path(), old)
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
		cmdErr = runCommand(command, base, vars)
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

// envVar is an environment variable that gets set to cassette's address plus path.
type envVar struct{ name, path string }

// Environment variables the official SDKs read their base URL from.
var defaultEnvVars = []envVar{
	{"ANTHROPIC_BASE_URL", ""},
	{"OPENAI_BASE_URL", "/v1"}, // the OpenAI SDK expects /v1 to be part of the base URL
}

// envVars combines the built-in variables with those from cassette.yaml and
// --env flags. For the same name, --env beats cassette.yaml beats built-in.
func envVars(fromFile map[string]string, flags []string) ([]envVar, error) {
	paths := map[string]string{}
	names := []string{}
	set := func(name, path string) error {
		if name == "" || strings.ContainsAny(name, " =") {
			return fmt.Errorf("invalid environment variable name %q", name)
		}
		if path != "" && !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		// Windows variable names ignore case, so treat them that way everywhere.
		key := strings.ToUpper(name)
		if _, seen := paths[key]; !seen {
			names = append(names, name)
		}
		paths[key] = path
		return nil
	}

	for _, v := range defaultEnvVars {
		set(v.name, v.path)
	}
	fileNames := make([]string, 0, len(fromFile))
	for name := range fromFile {
		fileNames = append(fileNames, name)
	}
	sort.Strings(fileNames) // map order is random; keep the log output stable
	for _, name := range fileNames {
		if err := set(name, fromFile[name]); err != nil {
			return nil, fmt.Errorf("cassette.yaml env: %w", err)
		}
	}
	for _, f := range flags {
		name, path, _ := strings.Cut(f, "=")
		if err := set(name, path); err != nil {
			return nil, fmt.Errorf("--env %s: %w", f, err)
		}
	}

	vars := make([]envVar, 0, len(names))
	for _, name := range names {
		vars = append(vars, envVar{name, paths[strings.ToUpper(name)]})
	}
	return vars, nil
}

// runCommand runs the user's command with the SDKs pointed at cassette and
// returns an exitCodeError if the command fails.
func runCommand(command []string, base string, vars []envVar) error {
	c := exec.Command(command[0], command[1:]...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	c.Env = commandEnv(os.Environ(), base, vars)

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

// commandEnv returns environ with vars pointing at cassette, replacing any
// values that were already set.
func commandEnv(environ []string, base string, vars []envVar) []string {
	env := make([]string, 0, len(environ)+len(vars))
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		replaced := false
		for _, v := range vars {
			// Windows variable names ignore case, so compare that way everywhere.
			if strings.EqualFold(name, v.name) {
				// Don't print the old value: some gateway addresses contain a token,
				// and CI logs are often public.
				log.Printf("replacing %s for the command", v.name)
				replaced = true
			}
		}
		if !replaced {
			env = append(env, kv)
		}
	}
	for _, v := range vars {
		env = append(env, v.name+"="+base+v.path)
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
