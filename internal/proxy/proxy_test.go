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
	"sync/atomic"
	"testing"
	"time"

	"github.com/TemirlanN-dev/cassette/internal/cassette"
	"github.com/TemirlanN-dev/cassette/internal/match"
)

// fakeAPI is a stand-in for the real API that counts how often it was called.
type fakeAPI struct {
	calls atomic.Int32
	url   *url.URL
}

func newFakeAPI(t *testing.T, handler http.HandlerFunc) *fakeAPI {
	t.Helper()
	f := &fakeAPI{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	f.url, _ = url.Parse(srv.URL)
	return f
}

// startProxy runs a proxy in the given mode and returns its URL.
func startProxy(t *testing.T, o Options) (string, *Proxy) {
	t.Helper()
	if o.Matcher == nil {
		m, err := match.New(match.Config{})
		if err != nil {
			t.Fatal(err)
		}
		o.Matcher = m
	}
	p := New(o)
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	return srv.URL, p
}

func tempCassette(t *testing.T) string {
	return filepath.Join(t.TempDir(), "test.json")
}

func post(t *testing.T, url, body string) (int, string) {
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
	return resp.StatusCode, string(out)
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

func jsonReply(reply string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, reply)
	}
}

// --- record ---

func TestRecordsJSONResponse(t *testing.T) {
	const reply = `{"content":[{"type":"text","text":"hi"}]}`
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"model":"m"}` {
			t.Errorf("fake API got body %q; the proxy must forward the body it read", body)
		}
		if r.Header.Get("X-Api-Key") != "sk-secret" {
			t.Error("fake API did not receive the API key")
		}
		jsonReply(reply)(w, r)
	})
	path := tempCassette(t)
	base, _ := startProxy(t, Options{Mode: Record, Target: api.url, Store: cassette.NewStore(path)})

	if _, got := post(t, base+"/v1/messages", `{"model":"m"}`); got != reply {
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

var streamEvents = []string{
	"event: message_start\ndata: {\"type\":\"message_start\"}\n\n",
	"event: content_block_delta\ndata: {\"text\":\"Привет\"}\n\n", // multi-byte characters
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
}

func streamReply(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range streamEvents {
		io.WriteString(w, e)
		w.(http.Flusher).Flush()
		time.Sleep(30 * time.Millisecond)
	}
}

func TestRecordsStreamAsTimedChunks(t *testing.T) {
	api := newFakeAPI(t, streamReply)
	path := tempCassette(t)
	base, _ := startProxy(t, Options{Mode: Record, Target: api.url, Store: cassette.NewStore(path)})

	want := strings.Join(streamEvents, "")
	if _, got := post(t, base+"/v1/messages", `{"stream":true}`); got != want {
		t.Fatalf("app got %q, want %q", got, want)
	}

	chunks := loadEventually(t, path, 1).Interactions[0].Response.Chunks
	if len(chunks) < len(streamEvents) {
		t.Fatalf("got %d chunks, want at least %d", len(chunks), len(streamEvents))
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
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"rate_limited"}`, http.StatusTooManyRequests)
	})
	path := tempCassette(t)
	base, _ := startProxy(t, Options{Mode: Record, Target: api.url, Store: cassette.NewStore(path)})
	post(t, base+"/v1/messages", `{}`)

	time.Sleep(100 * time.Millisecond)
	if c, err := cassette.Load(path); err == nil {
		t.Fatalf("a 429 response was saved: %+v", c.Interactions)
	}
}

func TestSkipsResponseWhenAppDisconnects(t *testing.T) {
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done() // never finish; wait until the app hangs up
	})
	path := tempCassette(t)
	base, _ := startProxy(t, Options{Mode: Record, Target: api.url, Store: cassette.NewStore(path)})

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

func TestRecordSavesRepeatedRequestOnce(t *testing.T) {
	api := newFakeAPI(t, jsonReply(`{"ok":true}`))
	path := tempCassette(t)
	base, _ := startProxy(t, Options{Mode: Record, Target: api.url, Store: cassette.NewStore(path)})

	post(t, base+"/v1/messages", `{"a":1}`)
	loadEventually(t, path, 1)
	post(t, base+"/v1/messages", `{"a":1}`)
	time.Sleep(100 * time.Millisecond)

	if n := len(loadEventually(t, path, 1).Interactions); n != 1 {
		t.Errorf("cassette has %d interactions, want 1", n)
	}
}

func TestKeyInURLIsForwardedButNotSaved(t *testing.T) {
	api := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "AIza-secret" {
			t.Error("the real API must still receive the key in the URL")
		}
		jsonReply(`{"ok":true}`)(w, r)
	})
	path := tempCassette(t)
	base, _ := startProxy(t, Options{Mode: Record, Target: api.url, Store: cassette.NewStore(path)})
	post(t, base+"/v1beta/models/m:generateContent?key=AIza-secret", `{}`)

	if got := loadEventually(t, path, 1).Interactions[0].Request.Path; got != "/v1beta/models/m:generateContent" {
		t.Errorf("saved path = %q; the key must not be saved", got)
	}
}

// --- auto ---

func TestAutoMatchesAcrossDifferentKeysInURL(t *testing.T) {
	api := newFakeAPI(t, jsonReply(`{"ok":true}`))
	path := tempCassette(t)
	store, _ := cassette.OpenStore(path, false)
	base, _ := startProxy(t, Options{Mode: Auto, Target: api.url, Store: store})

	post(t, base+"/v1beta/models/m:generateContent?key=alice", `{}`)
	loadEventually(t, path, 1)
	post(t, base+"/v1beta/models/m:generateContent?key=bob", `{}`)

	if n := api.calls.Load(); n != 1 {
		t.Errorf("real API called %d times; requests differing only by key should match", n)
	}
}

func TestAutoRecordsOnceThenReplays(t *testing.T) {
	const reply = `{"content":"hello"}`
	api := newFakeAPI(t, jsonReply(reply))
	path := tempCassette(t)
	store, _ := cassette.OpenStore(path, false)
	base, p := startProxy(t, Options{Mode: Auto, Target: api.url, Store: store})

	// Same request, different date: the default rules make these match.
	post(t, base+"/v1/messages", `{"system":"Today is 2026-10-17"}`)
	loadEventually(t, path, 1)
	_, got := post(t, base+"/v1/messages", `{"system":"Today is 2026-10-18"}`)

	if got != reply {
		t.Errorf("second request got %q, want the recorded %q", got, reply)
	}
	if n := api.calls.Load(); n != 1 {
		t.Errorf("real API called %d times, want 1 (second request should be replayed)", n)
	}
	if p.Unused() != 0 {
		t.Errorf("Unused() = %d, want 0", p.Unused())
	}
}

func TestAutoKeepsExistingRecordings(t *testing.T) {
	path := tempCassette(t)
	seed := cassette.NewStore(path)
	seed.Add(cassette.Interaction{
		Request:  cassette.Request{Method: "POST", Path: "/v1/messages", Body: cassette.EncodeBody([]byte(`{"old":true}`))},
		Response: cassette.Response{Status: 200, Chunks: []cassette.Chunk{{Data: "old answer"}}},
	})

	api := newFakeAPI(t, jsonReply(`{"new":true}`))
	store, _ := cassette.OpenStore(path, false)
	base, p := startProxy(t, Options{Mode: Auto, Target: api.url, Store: store})
	post(t, base+"/v1/messages", `{"new":true}`)

	if n := len(loadEventually(t, path, 2).Interactions); n != 2 {
		t.Errorf("cassette has %d interactions, want the old one plus the new one", n)
	}
	if p.Unused() != 1 {
		t.Errorf("Unused() = %d, want 1 (the old recording)", p.Unused())
	}
}

// --- replay ---

// seedStream writes a cassette with one recorded streamed response.
func seedStream(t *testing.T, offsets []int64) string {
	t.Helper()
	path := tempCassette(t)
	var chunks []cassette.Chunk
	for k, e := range streamEvents {
		chunks = append(chunks, cassette.Chunk{OffsetMs: offsets[k], Data: e})
	}
	s := cassette.NewStore(path)
	s.Add(cassette.Interaction{
		Request: cassette.Request{Method: "POST", Path: "/v1/messages", Body: cassette.EncodeBody([]byte(`{"stream":true}`))},
		Response: cassette.Response{
			Status:  200,
			Headers: http.Header{"Content-Type": {"text/event-stream"}},
			Chunks:  chunks,
		},
	})
	return path
}

func TestReplayServesRecordingWithoutTheAPI(t *testing.T) {
	path := seedStream(t, []int64{0, 100, 200})
	store, err := cassette.OpenStore(path, true)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := startProxy(t, Options{Mode: Replay, Store: store}) // no Target at all

	start := time.Now()
	status, got := post(t, base+"/v1/messages", `{"stream":true}`)
	if status != 200 || got != strings.Join(streamEvents, "") {
		t.Errorf("got %d %q", status, got)
	}
	if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
		t.Errorf("instant replay took %v; it should ignore the 200ms of recorded timing", elapsed)
	}
}

func TestReplayRealtimeKeepsTiming(t *testing.T) {
	path := seedStream(t, []int64{0, 100, 200})
	store, _ := cassette.OpenStore(path, true)
	base, _ := startProxy(t, Options{Mode: Replay, Store: store, Realtime: true})

	start := time.Now()
	post(t, base+"/v1/messages", `{"stream":true}`)
	if elapsed := time.Since(start); elapsed < 190*time.Millisecond {
		t.Errorf("realtime replay took %v, want about 200ms", elapsed)
	}
}

func TestReplayMissFailsLoudly(t *testing.T) {
	path := seedStream(t, []int64{0, 0, 0})
	store, _ := cassette.OpenStore(path, true)
	base, _ := startProxy(t, Options{Mode: Replay, Store: store})

	status, body := post(t, base+"/v1/messages", `{"different":"request"}`)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (SDKs retry 5xx)", status)
	}
	var e struct {
		Error struct{ Type, Message string }
	}
	if err := json.Unmarshal([]byte(body), &e); err != nil || e.Error.Type != "cassette_miss" || !strings.Contains(e.Error.Message, "cassette auto") {
		t.Errorf("error body = %s", body)
	}
}

func TestReplayNonJSONBody(t *testing.T) {
	api := newFakeAPI(t, jsonReply(`{"ok":true}`))
	path := tempCassette(t)
	base, _ := startProxy(t, Options{Mode: Record, Target: api.url, Store: cassette.NewStore(path)})
	post(t, base+"/upload", "plain text body")
	loadEventually(t, path, 1)

	store, _ := cassette.OpenStore(path, true)
	base, _ = startProxy(t, Options{Mode: Replay, Store: store})
	if status, _ := post(t, base+"/upload", "plain text body"); status != 200 {
		t.Errorf("non-JSON request was not found on replay (status %d)", status)
	}
}
