package cassette

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreSavesAndLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "c.json")
	s := NewStore(path)
	err := s.Add(Interaction{
		Request:  Request{Method: "POST", Path: "/v1/messages", Body: EncodeBody([]byte(`{"a":1}`))},
		Response: Response{Status: 200, Chunks: []Chunk{{OffsetMs: 0, Data: "hi"}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Interactions) != 1 || c.Interactions[0].Response.Chunks[0].Data != "hi" {
		t.Errorf("loaded %+v", c)
	}
}

func rec(path string) Interaction {
	return Interaction{
		Request:  Request{Method: "POST", Path: path, Body: EncodeBody([]byte(`{"prompt":"<hi> & bye"}`))},
		Response: Response{Status: 200, Chunks: []Chunk{{Data: "answer"}}},
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

func TestOneRecordingPerLineAppended(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.jsonl")
	s := NewStore(path)
	s.Add(rec("/one"))
	s.Add(rec("/two"))

	reopened, _ := OpenStore(path, true)
	reopened.Add(rec("/three")) // a later run appends to the same file

	lines := readLines(t, path)
	if len(lines) != 4 || lines[0] != `{"version":2}` {
		t.Fatalf("file = %q", lines)
	}
	if !strings.Contains(lines[1], `"/one"`) || !strings.Contains(lines[3], `"/three"`) {
		t.Errorf("recordings out of order: %q", lines)
	}
	if !strings.Contains(lines[1], `<hi> & bye`) {
		t.Errorf("prompt text should stay readable, got %s", lines[1])
	}
}

func TestReadsVersion1Files(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.json")
	os.WriteFile(path, []byte(`{
  "version": 1,
  "interactions": [
    {
      "request": { "method": "POST", "path": "/v1/messages", "body": { "a": 1 } },
      "response": { "status": 200, "chunks": [ { "offset_ms": 0, "data": "hi" } ] }
    }
  ]
}
`), 0o644)

	s, err := OpenStore(path, true)
	if err != nil || len(s.Interactions()) != 1 {
		t.Fatalf("OpenStore: %v, %d interactions", err, len(s.Interactions()))
	}
	s.Add(rec("/new")) // the first save converts the file
	if lines := readLines(t, path); len(lines) != 3 || lines[0] != `{"version":2}` {
		t.Errorf("file was not converted: %q", lines)
	}
}

func TestMigratesOldDefaultFileName(t *testing.T) {
	dir := t.TempDir()
	NewStore(filepath.Join(dir, "default.json")).Add(rec("/old")) // pretend this is a v1 file
	old := filepath.Join(dir, "default.json")
	c, _ := Load(old)
	c.Version = 1
	data, _ := json.Marshal(c)
	os.WriteFile(old, data, 0o644)

	s, err := OpenStore(filepath.Join(dir, "default.jsonl"), true)
	if err != nil {
		t.Fatal(err)
	}
	if s.MigratedFrom() != old || len(s.Interactions()) != 1 {
		t.Errorf("MigratedFrom = %q, %d interactions", s.MigratedFrom(), len(s.Interactions()))
	}
}

func TestHalfWrittenLastLineIsSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.jsonl")
	s := NewStore(path)
	s.Add(rec("/one"))
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"request":{"method":"PO`) // cassette was stopped mid-write
	f.Close()

	s, err := OpenStore(path, true)
	if err != nil || len(s.Interactions()) != 1 {
		t.Fatalf("OpenStore: %v, %d interactions", err, len(s.Interactions()))
	}
	s.Add(rec("/two")) // must not be glued onto the broken line
	c, err := Load(path)
	if err != nil || len(c.Interactions) != 2 {
		t.Errorf("after adding: %v, %+v", err, c)
	}
}

func TestBrokenLineInTheMiddleIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.jsonl")
	os.WriteFile(path, []byte("{\"version\":2}\nnot json\n{\"request\":{}}\n"), 0o644)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("err = %v, want it to point at line 2", err)
	}
}

func TestWindowsLineEndings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.jsonl")
	NewStore(path).Add(rec("/one"))
	data, _ := os.ReadFile(path)
	os.WriteFile(path, []byte(strings.ReplaceAll(string(data), "\n", "\r\n")), 0o644) // as Git checks it out on Windows

	if c, err := Load(path); err != nil || len(c.Interactions) != 1 {
		t.Errorf("Load: %v", err)
	}
}

func TestOpenStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")
	if _, err := OpenStore(path, true); err == nil {
		t.Error("a missing file should be an error when mustExist is true")
	}
	s, err := OpenStore(path, false)
	if err != nil || len(s.Interactions()) != 0 {
		t.Errorf("a missing file should give an empty store, got %v", err)
	}

	s.Add(Interaction{Request: Request{Method: "GET", Path: "/x"}})
	s, err = OpenStore(path, true)
	if err != nil || len(s.Interactions()) != 1 {
		t.Errorf("reopened store: %v, %d interactions", err, len(s.Interactions()))
	}
}

func TestCleanHeadersRemovesSecrets(t *testing.T) {
	h := http.Header{}
	h.Set("X-Api-Key", "sk-secret")
	h.Set("Authorization", "Bearer sk-secret")
	h.Set("X-Goog-Api-Key", "AIza-secret")
	h.Set("Anthropic-Organization-Id", "org-1234")
	h.Set("Content-Length", "42")
	h.Set("Content-Type", "application/json")

	c := CleanHeaders(h)
	for _, k := range []string{"X-Api-Key", "Authorization", "X-Goog-Api-Key", "Anthropic-Organization-Id", "Content-Length"} {
		if c.Get(k) != "" {
			t.Errorf("%s was kept", k)
		}
	}
	if c.Get("Content-Type") != "application/json" {
		t.Error("Content-Type was removed")
	}
	if h.Get("X-Api-Key") == "" {
		t.Error("CleanHeaders modified the original headers")
	}
}

func TestCleanPathRemovesKeyParams(t *testing.T) {
	cases := map[string]string{
		"/v1/messages": "/v1/messages",
		"/v1beta/models/m:generate?key=AIza-secret":  "/v1beta/models/m:generate",
		"/v1beta/models/m:stream?alt=sse&key=AIza-x": "/v1beta/models/m:stream?alt=sse",
		"/x?api_key=secret&beta=true":                "/x?beta=true",
	}
	for in, want := range cases {
		u, _ := url.Parse(in)
		if got := CleanPath(u); got != want {
			t.Errorf("CleanPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEncodeBody(t *testing.T) {
	if got := string(EncodeBody([]byte(`{"a":1}`))); got != `{"a":1}` {
		t.Errorf("JSON body = %s, want it stored as-is", got)
	}
	if got := string(EncodeBody([]byte("not json"))); got != `"not json"` {
		t.Errorf("text body = %s, want a JSON string", got)
	}
	if EncodeBody(nil) != nil {
		t.Error("empty body should be omitted")
	}
}
