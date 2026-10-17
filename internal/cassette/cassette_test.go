package cassette

import (
	"net/http"
	"net/url"
	"path/filepath"
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
