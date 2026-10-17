// Package proxy is the HTTP server that sits between the app and the real API.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"github.com/TemirlanN-dev/cassette/internal/cassette"
	"github.com/TemirlanN-dev/cassette/internal/match"
)

type Mode int

const (
	// Record forwards every request to the real API and saves the answers.
	Record Mode = iota
	// Auto answers known requests from the cassette and records unknown ones.
	Auto
	// Replay answers only from the cassette and fails on unknown requests.
	Replay
)

type Options struct {
	Mode     Mode
	Target   *url.URL // the real API; not used in Replay mode
	Store    *cassette.Store
	Matcher  *match.Matcher
	Realtime bool // replay streamed chunks with their original timing
}

type Proxy struct {
	mode     Mode
	store    *cassette.Store
	matcher  *match.Matcher
	realtime bool
	forward  *httputil.ReverseProxy

	mu     sync.Mutex
	index  map[string]cassette.Interaction // fingerprint -> recording
	used   map[string]bool                 // fingerprints answered or recorded this run
	misses int                             // requests Replay mode had no recording for
}

// ctxKey is used to attach the captured request to the request's context,
// so ModifyResponse can find it once the response comes back.
type ctxKey struct{}

type exchange struct {
	req         cassette.Request
	fingerprint string
}

func New(o Options) *Proxy {
	p := &Proxy{
		mode:     o.Mode,
		store:    o.Store,
		matcher:  o.Matcher,
		realtime: o.Realtime,
		index:    map[string]cassette.Interaction{},
		used:     map[string]bool{},
	}

	// Build the lookup table once, so finding a recording is a single map lookup.
	for _, i := range o.Store.Interactions() {
		fp := p.matcher.Fingerprint(i.Request.Method, i.Request.Path, i.Request.Body)
		if _, dup := p.index[fp]; dup {
			// Can happen when new rules make two old recordings look the same.
			log.Printf("note: two recordings match %s %s; using the first", i.Request.Method, i.Request.Path)
			continue
		}
		p.index[fp] = i
	}

	if o.Mode != Replay {
		p.forward = &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(o.Target)
				// Ask for an uncompressed response so the saved body is readable text.
				pr.Out.Header.Del("Accept-Encoding")
			},
			ModifyResponse: p.captureResponse,
			// Send each piece of a streamed response to the app as soon as it arrives.
			FlushInterval: -1,
		}
	}
	return p
}

// Loaded returns how many recordings were in the cassette at startup.
func (p *Proxy) Loaded() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.index)
}

// Unused returns how many recordings were neither replayed nor recorded this run.
func (p *Proxy) Unused() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.index) - len(p.used)
}

// Misses returns how many requests had no recording (Replay mode only).
func (p *Proxy) Misses() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.misses
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// A body can only be read once: read it, keep the bytes, and put back a
	// fresh reader over the same bytes so the proxy can still forward it.
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		http.Error(w, "cassette: reading request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	// The forwarded request keeps the full URL; only the saved and matched copy drops secrets.
	path := cassette.CleanPath(r.URL)
	fp := p.matcher.Fingerprint(r.Method, path, body)

	if p.mode != Record {
		if rec, ok := p.lookup(fp); ok {
			p.replay(w, r, rec)
			return
		}
		if p.mode == Replay {
			p.miss(w, r.Method, path)
			return
		}
	}

	ex := exchange{
		fingerprint: fp,
		req: cassette.Request{
			Method:  r.Method,
			Path:    path,
			Headers: cassette.CleanHeaders(r.Header),
			Body:    cassette.EncodeBody(body),
		},
	}
	p.forward.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, ex)))
}

func (p *Proxy) lookup(fp string) (cassette.Interaction, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	rec, ok := p.index[fp]
	if ok {
		p.used[fp] = true
	}
	return rec, ok
}

// replay sends a recorded response: instantly, or with the original timing if realtime is on.
func (p *Proxy) replay(w http.ResponseWriter, r *http.Request, rec cassette.Interaction) {
	for k, v := range rec.Response.Headers {
		w.Header()[k] = v
	}
	w.WriteHeader(rec.Response.Status)
	flusher, _ := w.(http.Flusher)

	start := time.Now()
	for _, c := range rec.Response.Chunks {
		if p.realtime {
			wait := time.Until(start.Add(time.Duration(c.OffsetMs) * time.Millisecond))
			select {
			case <-time.After(wait):
			case <-r.Context().Done(): // the app hung up; stop sending
				return
			}
		}
		io.WriteString(w, c.Data)
		if flusher != nil {
			flusher.Flush()
		}
	}
	log.Printf("replayed %s %s -> %d", rec.Request.Method, rec.Request.Path, rec.Response.Status)
}

// miss answers an unknown request in Replay mode. It uses 404 rather than 500
// because the official SDKs automatically retry 5xx responses, which would
// repeat every miss with delays. The body uses the shape both the Anthropic
// and OpenAI SDKs read error messages from.
func (p *Proxy) miss(w http.ResponseWriter, method, path string) {
	msg := "cassette: no recording for " + method + " " + path + " in " + p.store.Path() +
		" (run `cassette auto` to record it)"
	log.Printf("MISS %s %s: not in %s", method, path, p.store.Path())
	p.mu.Lock()
	p.misses++
	p.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": "cassette_miss", "message": msg},
	})
}

// captureResponse runs when the real API's response headers arrive. It swaps
// the body for one that copies everything passing through to the app.
func (p *Proxy) captureResponse(resp *http.Response) error {
	ex := resp.Request.Context().Value(ctxKey{}).(exchange)
	req := ex.req
	start := time.Now()
	status, headers := resp.StatusCode, cassette.CleanHeaders(resp.Header)

	resp.Body = &recordingBody{
		ReadCloser: resp.Body,
		start:      start,
		streaming:  isEventStream(resp.Header),
		finish: func(chunks []cassette.Chunk, complete bool) {
			switch {
			case !complete:
				log.Printf("not recorded: %s %s (app disconnected before the response finished)", req.Method, req.Path)
			case status < 200 || status > 299:
				// Saving a 401 or 429 would make replay reproduce a broken run forever.
				log.Printf("not recorded: %s %s returned %d", req.Method, req.Path, status)
			default:
				p.save(ex.fingerprint, cassette.Interaction{
					RecordedAt: start.UTC(),
					Request:    req,
					Response:   cassette.Response{Status: status, Headers: headers, Chunks: chunks},
				})
			}
		},
	}
	return nil
}

// save adds a recording unless an identical request was already saved, which
// can happen when the same request is sent twice in a row or at the same time.
func (p *Proxy) save(fp string, i cassette.Interaction) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.index[fp]; exists {
		log.Printf("already recorded: %s %s", i.Request.Method, i.Request.Path)
		return
	}
	if err := p.store.Add(i); err != nil {
		log.Printf("error saving %s: %v", p.store.Path(), err)
		return
	}
	p.index[fp] = i
	p.used[fp] = true
	log.Printf("recorded %s %s -> %d (%d chunks)", i.Request.Method, i.Request.Path, i.Response.Status, len(i.Response.Chunks))
}

func isEventStream(h http.Header) bool {
	mt, _, _ := mime.ParseMediaType(h.Get("Content-Type"))
	return mt == "text/event-stream"
}

// recordingBody passes the response through to the app unchanged while
// copying it into timestamped chunks.
type recordingBody struct {
	io.ReadCloser
	start     time.Time
	streaming bool
	finish    func(chunks []cassette.Chunk, complete bool)

	pending []byte // bytes read but not yet saved as a chunk
	chunks  []cassette.Chunk
	eof     bool // true once the whole body was read
	once    sync.Once
}

func (b *recordingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.pending = append(b.pending, p[:n]...)
		// A network read can stop anywhere, even in the middle of a character.
		// Only cut a chunk after the last line break and hold back the rest.
		if b.streaming {
			if i := bytes.LastIndexByte(b.pending, '\n'); i >= 0 {
				b.addChunk(b.pending[:i+1])
				b.pending = b.pending[i+1:]
			}
		}
	}
	if err == io.EOF {
		b.eof = true
	}
	return n, err
}

func (b *recordingBody) addChunk(data []byte) {
	var offset int64
	if b.streaming {
		offset = time.Since(b.start).Milliseconds()
	}
	b.chunks = append(b.chunks, cassette.Chunk{OffsetMs: offset, Data: string(data)})
}

// Close is called once the proxy is done with the response, whether it finished or not.
func (b *recordingBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() {
		if len(b.pending) > 0 {
			b.addChunk(b.pending)
		}
		b.finish(b.chunks, b.eof)
	})
	return err
}
