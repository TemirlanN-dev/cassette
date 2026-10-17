package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TemirlanN-dev/cassette/internal/cassette"
)

// startProxy runs a fake API and a recording proxy in front of it.
// It returns the proxy's URL and the cassette file path.
func startProxy(t *testing.T, fakeAPI http.HandlerFunc) (string, string) {
	t.Helper()
	api := httptest.NewServer(fakeAPI)
	t.Cleanup(api.Close)
	target, _ := url.Parse(api.URL)

	path := filepath.Join(t.TempDir(), "test.json")
	srv := httptest.NewServer(New(target, cassette.NewStore(path)))
	t.Cleanup(srv.Close)
	return srv.URL, path
}

func post(t *testing.T, url, body string) string {
	t.Helper()
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", "sk-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return string(out)
}

// loadEventually waits for the cassette to contain n interactions. Saving happens
// just after the app receives the response, so the file can lag slightly behind.
func loadEventually(t *testing.T, path string, n int) *cassette.Cassette {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		c, err := cassette.Load(path)
		if err == nil && len(c.Interactions) >= n {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("cassette never had %d interactions (last error: %v)", n, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRecordsJSONResponse(t *testing.T) {
	const reply = `{"content":[{"type":"text","text":"hi"}]}`
	base, path := startProxy(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"model":"m"}` {
			t.Errorf("fake API got body %q; the proxy must forward the body it read", body)
		}
		if r.Header.Get("X-Api-Key") != "sk-secret" {
			t.Error("fake API did not receive the API key")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, reply)
	})

	if got := post(t, base+"/v1/messages", `{"model":"m"}`); got != reply {
		t.Fatalf("app got %q, want %q", got, reply)
	}

	i := loadEventually(t, path, 1).Interactions[0]
	if i.Request.Method != "POST" || i.Request.Path != "/v1/messages" {
		t.Errorf("saved request = %s %s", i.Request.Method, i.Request.Path)
	}
	// The file is indented for readability, so compact the body before comparing.
	var body bytes.Buffer
	json.Compact(&body, i.Request.Body)
	if body.String() != `{"model":"m"}` {
		t.Errorf("saved request body = %s", i.Request.Body)
	}
	if i.Request.Headers.Get("X-Api-Key") != "" {
		t.Error("API key was saved to the cassette")
	}
	if i.Response.Status != 200 || len(i.Response.Chunks) != 1 || i.Response.Chunks[0].Data != reply {
		t.Errorf("saved response = %+v", i.Response)
	}
}

func TestRecordsStreamAsTimedChunks(t *testing.T) {
	events := []string{
		"event: message_start\ndata: {\"type\":\"message_start\"}\n\n",
		"event: content_block_delta\ndata: {\"text\":\"Привет\"}\n\n", // multi-byte characters
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}
	base, path := startProxy(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			io.WriteString(w, e)
			w.(http.Flusher).Flush()
			time.Sleep(30 * time.Millisecond)
		}
	})

	want := strings.Join(events, "")
	if got := post(t, base+"/v1/messages", `{"stream":true}`); got != want {
		t.Fatalf("app got %q, want %q", got, want)
	}

	chunks := loadEventually(t, path, 1).Interactions[0].Response.Chunks
	if len(chunks) < len(events) {
		t.Fatalf("got %d chunks, want at least %d", len(chunks), len(events))
	}
	var joined strings.Builder
	for k, c := range chunks {
		joined.WriteString(c.Data)
		if !strings.HasSuffix(c.Data, "\n") {
			t.Errorf("chunk %d does not end at a line break: %q", k, c.Data)
		}
		if k > 0 && c.OffsetMs < chunks[k-1].OffsetMs {
			t.Errorf("chunk %d offset %d is before the previous one (%d)", k, c.OffsetMs, chunks[k-1].OffsetMs)
		}
	}
	if joined.String() != want {
		t.Errorf("chunks joined = %q, want %q", joined.String(), want)
	}
	if last := chunks[len(chunks)-1].OffsetMs; last < 50 {
		t.Errorf("last chunk offset is %dms; the fake API waited at least 60ms", last)
	}
}

func TestSkipsErrorResponses(t *testing.T) {
	base, path := startProxy(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"rate_limited"}`, http.StatusTooManyRequests)
	})
	post(t, base+"/v1/messages", `{}`)

	time.Sleep(100 * time.Millisecond)
	if c, err := cassette.Load(path); err == nil {
		t.Fatalf("a 429 response was saved: %+v", c.Interactions)
	}
}

func TestSkipsResponseWhenAppDisconnects(t *testing.T) {
	base, path := startProxy(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done() // never finish; wait until the app hangs up
	})

	resp, err := http.Post(base+"/v1/messages", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	io.ReadFull(resp.Body, buf) // read part of the stream, then hang up
	resp.Body.Close()

	time.Sleep(200 * time.Millisecond)
	if c, err := cassette.Load(path); err == nil {
		t.Fatalf("an incomplete response was saved: %+v", c.Interactions)
	}
}
