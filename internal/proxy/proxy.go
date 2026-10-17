// Package proxy is the HTTP server that sits between the app and the real API.
package proxy

import (
	"bytes"
	"context"
	"io"
	"log"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"github.com/TemirlanN-dev/cassette/internal/cassette"
)

// Proxy forwards every request to the real API and saves each successful exchange to a Store.
type Proxy struct {
	store   *cassette.Store
	forward *httputil.ReverseProxy
}

// ctxKey is used to attach the captured request to the request's context,
// so ModifyResponse can find it once the response comes back.
type ctxKey struct{}

func New(target *url.URL, store *cassette.Store) *Proxy {
	p := &Proxy{store: store}
	p.forward = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			// Ask for an uncompressed response so the saved body is readable text.
			pr.Out.Header.Del("Accept-Encoding")
		},
		ModifyResponse: p.captureResponse,
		// Send each piece of a streamed response to the app as soon as it arrives.
		FlushInterval: -1,
	}
	return p
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

	req := cassette.Request{
		Method:  r.Method,
		Path:    r.URL.RequestURI(),
		Headers: cassette.CleanHeaders(r.Header),
		Body:    cassette.EncodeBody(body),
	}
	p.forward.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, req)))
}

// captureResponse runs when the real API's response headers arrive. It swaps
// the body for one that copies everything passing through to the app.
func (p *Proxy) captureResponse(resp *http.Response) error {
	req := resp.Request.Context().Value(ctxKey{}).(cassette.Request)
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
				err := p.store.Add(cassette.Interaction{
					RecordedAt: start.UTC(),
					Request:    req,
					Response:   cassette.Response{Status: status, Headers: headers, Chunks: chunks},
				})
				if err != nil {
					log.Printf("error saving %s: %v", p.store.Path(), err)
					return
				}
				log.Printf("recorded %s %s -> %d (%d chunks)", req.Method, req.Path, status, len(chunks))
			}
		},
	}
	return nil
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
