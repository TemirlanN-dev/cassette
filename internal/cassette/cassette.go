// Package cassette defines the recording file format and how it is saved.
package cassette

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FormatVersion is bumped whenever the file layout changes in a way old files can't be read.
const FormatVersion = 1

// Cassette is the whole recording file.
type Cassette struct {
	Version      int           `json:"version"`
	Interactions []Interaction `json:"interactions"`
}

// Interaction is one request and the response the real API gave to it.
type Interaction struct {
	RecordedAt time.Time `json:"recorded_at"`
	Request    Request   `json:"request"`
	Response   Response  `json:"response"`
}

type Request struct {
	Method  string          `json:"method"`
	Path    string          `json:"path"`
	Headers http.Header     `json:"headers,omitempty"`
	Body    json.RawMessage `json:"body,omitempty"`
}

type Response struct {
	Status  int         `json:"status"`
	Headers http.Header `json:"headers,omitempty"`
	Chunks  []Chunk     `json:"chunks"`
}

// Chunk is a piece of the response body and when it arrived, in milliseconds
// after the response started. A normal response is one chunk at 0; a streamed
// response is many, which lets replay reproduce the original timing.
type Chunk struct {
	OffsetMs int64  `json:"offset_ms"`
	Data     string `json:"data"`
}

// Secrets that must never be written to disk, since cassette files are committed to git.
var secretHeaders = []string{
	"Authorization",
	"X-Api-Key",
	"Api-Key",
	"Openai-Organization",
	"Openai-Project",
	"Cookie",
	"Set-Cookie",
}

// Headers about how bytes were sent rather than what they mean; they'd be wrong on replay.
var transportHeaders = []string{
	"Accept-Encoding",
	"Connection",
	"Content-Encoding",
	"Content-Length",
	"Transfer-Encoding",
}

// CleanHeaders returns a copy of h without secrets or transport headers.
func CleanHeaders(h http.Header) http.Header {
	c := h.Clone()
	for _, k := range secretHeaders {
		c.Del(k)
	}
	for _, k := range transportHeaders {
		c.Del(k)
	}
	return c
}

// EncodeBody stores a JSON body as real JSON, so it stays readable in git diffs,
// and anything else as a JSON string.
func EncodeBody(b []byte) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	if json.Valid(b) {
		return json.RawMessage(b)
	}
	s, _ := json.Marshal(string(b))
	return s
}

func Load(path string) (*Cassette, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Cassette
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.Version != FormatVersion {
		return nil, fmt.Errorf("%s: unsupported cassette version %d", path, c.Version)
	}
	return &c, nil
}

// Save writes to a temporary file and then renames it over the real one,
// so a crash mid-write never leaves a half-written cassette.
func (c *Cassette) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Store holds a cassette in memory and saves it to disk after every change.
// Its methods are safe to call from many requests at once.
type Store struct {
	mu   sync.Mutex
	path string
	c    Cassette
}

// NewStore starts an empty cassette at path. The file is replaced on the first Add.
func NewStore(path string) *Store {
	return &Store{path: path, c: Cassette{Version: FormatVersion, Interactions: []Interaction{}}}
}

func (s *Store) Path() string { return s.path }

func (s *Store) Add(i Interaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.c.Interactions = append(s.c.Interactions, i)
	return s.c.Save(s.path)
}
