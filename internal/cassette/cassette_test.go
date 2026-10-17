package cassette

import (
	"net/http"
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

func TestCleanHeadersRemovesSecrets(t *testing.T) {
	h := http.Header{}
	h.Set("X-Api-Key", "sk-secret")
	h.Set("Authorization", "Bearer sk-secret")
	h.Set("Content-Length", "42")
	h.Set("Content-Type", "application/json")

	c := CleanHeaders(h)
	for _, k := range []string{"X-Api-Key", "Authorization", "Content-Length"} {
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
