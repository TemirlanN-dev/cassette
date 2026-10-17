package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
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

func main() {
	root := &cobra.Command{
		Use:          "cassette",
		Short:        "Record and replay LLM API calls for fast, free, deterministic tests",
		SilenceUsage: true,
	}
	// Persistent flags are inherited by every subcommand.
	root.PersistentFlags().IntVar(&port, "port", 4000, "port to listen on")
	root.PersistentFlags().StringVar(&target, "target", "", "API to forward requests to, e.g. https://api.anthropic.com (required for record and auto)")
	root.PersistentFlags().StringVar(&cassettePath, "cassette", "cassettes/default.json", "cassette file to read and write")
	root.PersistentFlags().StringVar(&configPath, "config", "cassette.yaml", "matching rules file (optional)")

	root.AddCommand(
		newCommand(proxy.Record, "record", "Forward every request to the real API and save the answers to a fresh cassette"),
		newCommand(proxy.Auto, "auto", "Answer known requests from the cassette and record unknown ones"),
		newCommand(proxy.Replay, "replay", "Answer only from the cassette; fail on any request that wasn't recorded"),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func newCommand(mode proxy.Mode, name, short string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   name,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(mode)
		},
	}
	if mode != proxy.Record {
		cmd.Flags().BoolVar(&realtime, "realtime", false, "replay streamed responses with their original timing instead of instantly")
	}
	return cmd
}

func run(mode proxy.Mode) error {
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

	if err := serve(p); err != nil {
		return err
	}
	if mode != proxy.Record {
		if n := p.Unused(); n > 0 {
			log.Printf("%d recordings were not used this run", n)
		}
	}
	return nil
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

// serve runs the server until Ctrl+C, then lets in-flight requests finish.
func serve(handler http.Handler) error {
	// 127.0.0.1 means only this machine can connect; the proxy forwards API keys.
	srv := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", port), Handler: handler}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Printf("listening on http://%s (Ctrl+C to stop)", srv.Addr)

	select {
	case err := <-errc: // could not start, e.g. the port is taken
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
