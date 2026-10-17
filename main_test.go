package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/TemirlanN-dev/cassette/internal/cassette"
	"github.com/TemirlanN-dev/cassette/internal/proxy"
)

// TestHelperCommand isn't a real test. The other tests run this test binary
// again as their "test command" (instead of something like npm test), with
// CASSETTE_HELPER=1 set. It sends one request through cassette, like an app
// using the Anthropic SDK would, then exits with HELPER_EXIT.
func TestHelperCommand(t *testing.T) {
	if os.Getenv("CASSETTE_HELPER") != "1" {
		t.Skip("only runs as a child process")
	}
	if marker := os.Getenv("HELPER_MARKER"); marker != "" {
		os.WriteFile(marker, nil, 0o644) // lets a test check whether the command ran at all
	}
	resp, err := http.Post(os.Getenv("ANTHROPIC_BASE_URL")+"/v1/messages", "application/json", strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		os.Exit(99)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	code, _ := strconv.Atoi(os.Getenv("HELPER_EXIT"))
	os.Exit(code)
}

// helperCommand returns a command that runs TestHelperCommand and exits with code.
func helperCommand(t *testing.T, code int) []string {
	t.Setenv("CASSETTE_HELPER", "1")
	t.Setenv("HELPER_EXIT", strconv.Itoa(code))
	return []string{os.Args[0], "-test.run=^TestHelperCommand$"}
}

func setFlags(t *testing.T, targetURL string) {
	t.Helper()
	port = 4000
	target = targetURL
	cassettePath = filepath.Join(t.TempDir(), "c.json")
	configPath = filepath.Join(t.TempDir(), "no-config.yaml")
	realtime = false
}

func exitCode(err error) int {
	var e exitCodeError
	if errors.As(err, &e) {
		return e.code
	}
	if err != nil {
		return -1
	}
	return 0
}

func TestRunsCommandAndRecordsIt(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	}))
	defer api.Close()
	setFlags(t, api.URL)

	if err := run(proxy.Record, helperCommand(t, 0), false); err != nil {
		t.Fatalf("run: %v", err)
	}
	c, err := cassette.Load(cassettePath)
	if err != nil || len(c.Interactions) != 1 {
		t.Fatalf("cassette after run: %v, %+v", err, c)
	}
}

func TestPassesThroughCommandExitCode(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{}`)
	}))
	defer api.Close()
	setFlags(t, api.URL)

	if got := exitCode(run(proxy.Auto, helperCommand(t, 3), false)); got != 3 {
		t.Errorf("exit code = %d, want the command's 3", got)
	}
}

func TestReplayMissFailsEvenIfCommandPasses(t *testing.T) {
	setFlags(t, "")
	cassette.NewStore(cassettePath).Save() // an empty cassette: every request is a miss

	if got := exitCode(run(proxy.Replay, helperCommand(t, 0), false)); got != 1 {
		t.Errorf("exit code = %d, want 1 because of the miss", got)
	}
}

func TestCommandDoesNotRunIfPortIsTaken(t *testing.T) {
	setFlags(t, "")
	cassette.NewStore(cassettePath).Save()
	busy := httptest.NewServer(http.NotFoundHandler())
	defer busy.Close()
	port, _ = strconv.Atoi(busy.URL[strings.LastIndex(busy.URL, ":")+1:])

	marker := filepath.Join(t.TempDir(), "ran")
	t.Setenv("HELPER_MARKER", marker)

	err := run(proxy.Replay, helperCommand(t, 0), true) // portChosen: use the busy port
	if err == nil || exitCode(err) != -1 {
		t.Errorf("run = %v, want a listen error", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("the command ran even though cassette could not start")
	}
}

func TestCommandEnvReplacesBaseURLs(t *testing.T) {
	env := commandEnv([]string{
		"PATH=/bin",
		"anthropic_base_url=https://gateway.example.com", // different case, as Windows allows
		"OPENAI_BASE_URL=https://old",
	}, "http://127.0.0.1:5555")

	want := map[string]bool{
		"PATH=/bin": true,
		"ANTHROPIC_BASE_URL=http://127.0.0.1:5555": true,
		"OPENAI_BASE_URL=http://127.0.0.1:5555/v1": true,
	}
	if len(env) != len(want) {
		t.Fatalf("env = %v", env)
	}
	for _, kv := range env {
		if !want[kv] {
			t.Errorf("unexpected %q in %v", kv, env)
		}
	}
}
