package main

import (
	"bytes"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/TemirlanN-dev/cassette/internal/cassette"
	"github.com/TemirlanN-dev/cassette/internal/proxy"
)

func answer(text string) cassette.Interaction {
	return cassette.Interaction{
		Request: cassette.Request{Method: "POST", Path: "/v1/messages", Body: cassette.EncodeBody([]byte(`{"q":1}`))},
		Response: cassette.Response{Status: 200, Headers: http.Header{"Content-Type": {"application/json"}},
			Chunks: []cassette.Chunk{{Data: `{"type":"message","content":[{"type":"text","text":"` + text + `"}]}`}}},
	}
}

func writeCassette(t *testing.T, path string, recs ...cassette.Interaction) {
	t.Helper()
	s := cassette.NewStore(path)
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		s.Add(r)
	}
}

func TestDiffTwoFiles(t *testing.T) {
	setFlags(t, "")
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.jsonl"), filepath.Join(dir, "b.jsonl")
	writeCassette(t, a, answer("Hello"))
	writeCassette(t, b, answer("Hello"))
	if err := runDiff([]string{a, b}); err != nil {
		t.Errorf("identical files: %v, want no error (exit code 0)", err)
	}

	writeCassette(t, b, answer("Goodbye"))
	if got := exitCode(runDiff([]string{a, b})); got != 1 {
		t.Errorf("exit code = %d, want 1 when something changed", got)
	}
}

func TestDiffAgainstLastCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	setFlags(t, "")
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-c", "user.name=test", "-c", "user.email=test@example.com"}, args...)...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	cassettePath = filepath.Join(dir, "cassettes", "default.jsonl")
	writeCassette(t, cassettePath, answer("Hello"))
	git("add", ".")
	git("commit", "-q", "-m", "record")

	if err := runDiff(nil); err != nil {
		t.Errorf("right after committing: %v, want no changes", err)
	}
	writeCassette(t, cassettePath, answer("Hello"), answer("Extra"))
	if got := exitCode(runDiff(nil)); got != 1 {
		t.Errorf("exit code = %d, want 1 after re-recording", got)
	}
}

func TestOldDefaultFileIsConverted(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{}`)
	}))
	defer api.Close()
	setFlags(t, api.URL)
	dir := t.TempDir()
	old := filepath.Join(dir, "default.json")
	os.WriteFile(old, []byte(`{"version":1,"interactions":[{"request":{"method":"GET","path":"/old"},"response":{"status":200,"chunks":[]}}]}`), 0o644)
	cassettePath = filepath.Join(dir, "default.jsonl")

	if err := run(proxy.Auto, helperCommand(t, 0), false); err != nil {
		t.Fatalf("run: %v", err)
	}
	c, err := cassette.Load(cassettePath)
	if err != nil || len(c.Interactions) != 2 {
		t.Errorf("new file should hold the old recording plus the new one: %v, %+v", err, c)
	}
}

// TestHelperCommand isn't a real test. The other tests run this test binary
// again as their "test command" (instead of something like npm test), with
// CASSETTE_HELPER=1 set. It sends one request through cassette, like an app
// using the Anthropic SDK would, then exits with HELPER_EXIT. HELPER_URL_VAR
// makes it read its address from a different variable, like another SDK would.
func TestHelperCommand(t *testing.T) {
	if os.Getenv("CASSETTE_HELPER") != "1" {
		t.Skip("only runs as a child process")
	}
	if marker := os.Getenv("HELPER_MARKER"); marker != "" {
		os.WriteFile(marker, nil, 0o644) // lets a test check whether the command ran at all
	}
	urlVar, path := "ANTHROPIC_BASE_URL", "/v1/messages"
	if v := os.Getenv("HELPER_URL_VAR"); v != "" {
		urlVar, path = v, "/messages"
	}
	resp, err := http.Post(os.Getenv(urlVar)+path, "application/json", strings.NewReader(`{"prompt":"hi"}`))
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
	envFlags = nil
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
	}, "http://127.0.0.1:5555", defaultEnvVars)

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

func TestCommandEnvDoesNotLogOldValue(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	commandEnv([]string{"ANTHROPIC_BASE_URL=https://gateway.example.com/?token=secret"}, "http://127.0.0.1:5555", defaultEnvVars)
	if strings.Contains(logs.String(), "secret") {
		t.Errorf("the old value was logged: %s", logs.String())
	}
}

func TestEnvVarsCombinesDefaultsFileAndFlags(t *testing.T) {
	vars, err := envVars(
		map[string]string{"MISTRAL_BASE_URL": "/v1", "GROQ_BASE_URL": "/openai/v1"},
		[]string{"mistral_base_url=v2", "MY_AI_URL"}, // the flag wins, a missing "/" is added, the path is optional
	)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, v := range vars {
		got[strings.ToUpper(v.name)] = v.path
	}
	want := map[string]string{
		"ANTHROPIC_BASE_URL": "",
		"OPENAI_BASE_URL":    "/v1",
		"GROQ_BASE_URL":      "/openai/v1",
		"MISTRAL_BASE_URL":   "/v2",
		"MY_AI_URL":          "",
	}
	if len(got) != len(want) {
		t.Fatalf("vars = %+v", vars)
	}
	for name, path := range want {
		if p, ok := got[name]; !ok || p != path {
			t.Errorf("%s = %q (present %v), want %q", name, p, ok, path)
		}
	}
}

func TestEnvVarsRejectsBadNames(t *testing.T) {
	for _, flag := range []string{"=/v1", "MY VAR=/v1"} {
		if _, err := envVars(nil, []string{flag}); err == nil {
			t.Errorf("--env %q should be an error", flag)
		}
	}
}

func TestEnvFlagPointsAnyLibraryAtCassette(t *testing.T) {
	var gotPath string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		io.WriteString(w, `{}`)
	}))
	defer api.Close()
	setFlags(t, api.URL)
	envFlags = []string{"MY_AI_URL=/v1"}
	t.Setenv("HELPER_URL_VAR", "MY_AI_URL") // the "app" reads its address from MY_AI_URL

	if err := run(proxy.Record, helperCommand(t, 0), false); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotPath != "/v1/messages" {
		t.Errorf("the AI got a request for %q, want /v1/messages", gotPath)
	}
}
