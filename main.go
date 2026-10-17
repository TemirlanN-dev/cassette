package main

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"

	"github.com/spf13/cobra"

	"github.com/TemirlanN-dev/cassette/internal/cassette"
	"github.com/TemirlanN-dev/cassette/internal/proxy"
)

// Flags shared by every command.
var (
	port         int
	target       string
	cassettePath string
)

func main() {
	root := &cobra.Command{
		Use:          "cassette",
		Short:        "Record and replay LLM API calls for fast, free, deterministic tests",
		SilenceUsage: true,
	}
	// Persistent flags are inherited by every subcommand.
	root.PersistentFlags().IntVar(&port, "port", 4000, "port to listen on")
	root.PersistentFlags().StringVar(&target, "target", "", "API to forward requests to, e.g. https://api.anthropic.com (required)")
	root.PersistentFlags().StringVar(&cassettePath, "cassette", "cassettes/default.json", "cassette file to save to")

	root.AddCommand(&cobra.Command{
		Use:   "record",
		Short: "Forward every request to the real API and save the responses to a fresh cassette",
		RunE: func(cmd *cobra.Command, args []string) error {
			targetURL, err := parseTarget(target)
			if err != nil {
				return err
			}
			store := cassette.NewStore(cassettePath)
			return serve(proxy.New(targetURL, store), targetURL, "recording into "+store.Path())
		},
	})

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
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

func serve(handler http.Handler, targetURL *url.URL, what string) error {
	// 127.0.0.1 means only this machine can connect; the proxy forwards API keys.
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	log.Printf("listening on http://%s -> %s, %s", addr, targetURL, what)
	return http.ListenAndServe(addr, handler)
}
