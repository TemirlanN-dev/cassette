// Package cassette defines the recording file format and how it is saved.
//
// A cassette file holds one JSON object per line: a header line with the
// format version, then one line per recording. New recordings are simply
// added to the end. Files in the original format (version 1, one indented
// JSON object) can still be read, and are converted on the next save.
package cassette

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FormatVersion is bumped whenever the file layout changes.
const FormatVersion = 2

// Cassette is the whole recording file.
type Cassette struct {
	Version      int           `json:"version"`
	Interactions []Interaction `json:"interactions"`

	// brokenTail is set when the last line couldn't be read, e.g. because
	// cassette was stopped while adding it. The next save rewrites the file.
	brokenTail bool
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

// Secrets and account identifiers that must never be written to disk, since
// cassette files are committed to git.
var secretHeaders = []string{
	"Authorization",
	"X-Api-Key",
	"Api-Key",
	"X-Goog-Api-Key",
	"Anthropic-Organization-Id",
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

// Query parameters that carry API keys, e.g. Gemini's ?key=...
var secretParams = []string{"key", "api_key"}

// CleanPath returns the path and query of u without secret query parameters.
// It is used both for saving and for matching, so people with different keys
// still match each other's recordings.
func CleanPath(u *url.URL) string {
	q := u.Query()
	for _, k := range secretParams {
		q.Del(k)
	}
	if len(q) == 0 {
		return u.EscapedPath()
	}
	return u.EscapedPath() + "?" + q.Encode()
}

// EncodeBody stores a JSON body as real JSON and anything else as a JSON string.
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
	c, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Parse reads cassette data in the current format or the original version 1 format.
func Parse(data []byte) (*Cassette, error) {
	first, rest, _ := bytes.Cut(data, []byte("\n"))
	var h struct {
		Version int `json:"version"`
	}
	if json.Unmarshal(bytes.TrimSpace(first), &h) == nil && h.Version == FormatVersion {
		return parseLines(rest)
	}

	var c Cassette
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("not a cassette file: %w", err)
	}
	if c.Version != 1 {
		return nil, fmt.Errorf("unsupported cassette version %d", c.Version)
	}
	if c.Interactions == nil {
		c.Interactions = []Interaction{}
	}
	return &c, nil
}

func parseLines(data []byte) (*Cassette, error) {
	c := &Cassette{Version: FormatVersion, Interactions: []Interaction{}}
	lines := bytes.Split(data, []byte("\n"))
	for n, line := range lines {
		// TrimSpace also drops the \r that Git adds on Windows checkouts.
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var i Interaction
		if err := json.Unmarshal(line, &i); err != nil {
			if onlyBlankAfter(lines, n) {
				// A half-written last line: cassette was stopped while adding it.
				// Everything before it is fine, so keep that.
				c.brokenTail = true
				break
			}
			return nil, fmt.Errorf("line %d: %w", n+2, err) // +2: the header is line 1
		}
		c.Interactions = append(c.Interactions, i)
	}
	return c, nil
}

func onlyBlankAfter(lines [][]byte, n int) bool {
	for _, l := range lines[n+1:] {
		if len(bytes.TrimSpace(l)) > 0 {
			return false
		}
	}
	return true
}

// Save writes the whole cassette to a temporary file and then renames it over
// the real one, so a crash mid-write never leaves a half-written cassette.
func (c *Cassette) Save(path string) error {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "{\"version\":%d}\n", FormatVersion)
	enc := newLineEncoder(&buf)
	for _, i := range c.Interactions {
		if err := enc.Encode(i); err != nil {
			return err
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// newLineEncoder writes each value as one line of JSON. Characters like < and >
// are kept as they are instead of being escaped, which keeps prompts readable.
func newLineEncoder(buf *bytes.Buffer) *json.Encoder {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	return enc
}

// Store holds a cassette in memory and saves it to disk after every change.
// Its methods are safe to call from many requests at once.
type Store struct {
	mu           sync.Mutex
	path         string
	c            Cassette
	rewrite      bool   // the next Add must write the whole file, not just append
	migratedFrom string // set when the recordings came from an old-format file elsewhere
}

// NewStore starts an empty cassette at path. Nothing is written until Save or Add.
func NewStore(path string) *Store {
	return &Store{path: path, c: Cassette{Version: FormatVersion, Interactions: []Interaction{}}, rewrite: true}
}

// OpenStore loads the cassette at path. If the file doesn't exist, it looks
// for an old-format file with the previous name (default.json for
// default.jsonl). Failing that, it returns an error when mustExist is true and
// an empty store otherwise.
func OpenStore(path string, mustExist bool) (*Store, error) {
	c, err := Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		if old := strings.TrimSuffix(path, "l"); strings.HasSuffix(path, ".jsonl") {
			if oc, oldErr := Load(old); oldErr == nil {
				return &Store{path: path, c: *oc, rewrite: true, migratedFrom: old}, nil
			}
		}
		if mustExist {
			return nil, fmt.Errorf("%s not found; record it first with `cassette auto` or `cassette record`", path)
		}
		return NewStore(path), nil
	}
	if err != nil {
		return nil, err
	}
	return &Store{path: path, c: *c, rewrite: c.Version != FormatVersion || c.brokenTail}, nil
}

func (s *Store) Path() string { return s.path }

// MigratedFrom returns the old-format file the recordings were read from, if any.
func (s *Store) MigratedFrom() string { return s.migratedFrom }

// Interactions returns a copy of the list, so callers can't change it behind the lock's back.
func (s *Store) Interactions() []Interaction {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Interaction(nil), s.c.Interactions...)
}

// Save writes the whole cassette in the current format.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	if err := s.c.Save(s.path); err != nil {
		return err
	}
	s.rewrite = false
	return nil
}

// Add records one more interaction by adding a line to the end of the file.
func (s *Store) Add(i Interaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.c.Interactions = append(s.c.Interactions, i)
	if s.rewrite {
		return s.saveLocked()
	}

	var line bytes.Buffer
	if err := newLineEncoder(&line).Encode(i); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		// The file is gone (deleted while cassette was running?); write it fresh.
		return s.saveLocked()
	}
	_, err = f.Write(line.Bytes())
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}
