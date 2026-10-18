// Package match decides whether two requests count as "the same" by turning
// each one into a fingerprint. Requests with equal fingerprints get the same
// recorded answer.
package match

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Config holds the matching rules from cassette.yaml.
type Config struct {
	// DefaultRules turns the built-in UUID, timestamp and date rules on or off.
	// It is a pointer so a missing setting (nil) can mean "on".
	DefaultRules *bool         `yaml:"default_rules"`
	Ignore       []string      `yaml:"ignore"`
	Replace      []Replacement `yaml:"replace"`
}

// Replacement swaps every match of Pattern (a regular expression) for With
// inside any string in the request body.
type Replacement struct {
	Pattern string `yaml:"pattern"`
	With    string `yaml:"with"`
}

// Values that usually change on every run. Timestamps come before dates,
// since every timestamp also contains a date.
var defaultReplacements = []Replacement{
	{Pattern: `[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`, With: "<uuid>"},
	{Pattern: `\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}:?\d{2})?`, With: "<timestamp>"},
	{Pattern: `\d{4}-\d{2}-\d{2}`, With: "<date>"},
}

// Matcher computes fingerprints using a set of rules.
type Matcher struct {
	ignore  [][]string // each path split on dots, e.g. ["messages", "*", "id"]
	replace []compiledReplacement
}

type compiledReplacement struct {
	re   *regexp.Regexp
	with string
}

// New checks and prepares the rules once, so computing a fingerprint is fast.
func New(cfg Config) (*Matcher, error) {
	m := &Matcher{}
	for _, p := range cfg.Ignore {
		segments := strings.Split(p, ".")
		for _, s := range segments {
			if s == "" {
				return nil, fmt.Errorf("ignore rule %q has an empty part", p)
			}
		}
		m.ignore = append(m.ignore, segments)
	}

	var replacements []Replacement
	if cfg.DefaultRules == nil || *cfg.DefaultRules {
		replacements = append(replacements, defaultReplacements...)
	}
	replacements = append(replacements, cfg.Replace...)
	for _, r := range replacements {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return nil, fmt.Errorf("replace rule %q: %w", r.Pattern, err)
		}
		m.replace = append(m.replace, compiledReplacement{re: re, with: r.With})
	}
	return m, nil
}

// Fingerprint returns a short code that is equal for two requests exactly when
// they count as the same: same method, same path, and same body once the rules
// have been applied and JSON key order no longer matters.
func (m *Matcher) Fingerprint(method, path string, body []byte) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n", method, path)
	h.Write(m.Normalize(body))
	return hex.EncodeToString(h.Sum(nil))
}

// Normalize returns the body with the rules applied, in a canonical form.
func (m *Matcher) Normalize(body []byte) []byte {
	var v any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber() // keep numbers exactly as written instead of converting to float
	if err := dec.Decode(&v); err != nil {
		// Not JSON: compare it as plain text.
		return []byte(m.replaceText(string(body)))
	}
	if s, ok := v.(string); ok {
		// The cassette stores non-JSON bodies as a JSON string, so treat a string
		// the same as plain text; otherwise recordings and live requests wouldn't match.
		return []byte(m.replaceText(s))
	}
	for _, segments := range m.ignore {
		removePath(v, segments)
	}
	v = m.replaceStrings(v)
	// json.Marshal writes map keys in sorted order, so {"b":1,"a":2} and
	// {"a":2,"b":1} come out identical.
	out, _ := json.Marshal(v)
	return out
}

// removePath deletes the field named by segments. "*" means every item of a list.
func removePath(v any, segments []string) {
	seg, rest := segments[0], segments[1:]
	switch node := v.(type) {
	case map[string]any:
		if len(rest) == 0 {
			delete(node, seg)
		} else if child, ok := node[seg]; ok {
			removePath(child, rest)
		}
	case []any:
		if seg == "*" && len(rest) > 0 {
			for _, item := range node {
				removePath(item, rest)
			}
		}
	}
}

// replaceStrings applies the replace rules to every string value, at any depth.
func (m *Matcher) replaceStrings(v any) any {
	switch node := v.(type) {
	case string:
		return m.replaceText(node)
	case map[string]any:
		for k, child := range node {
			node[k] = m.replaceStrings(child)
		}
	case []any:
		for i, child := range node {
			node[i] = m.replaceStrings(child)
		}
	}
	return v
}

func (m *Matcher) replaceText(s string) string {
	for _, r := range m.replace {
		s = r.re.ReplaceAllString(s, r.with)
	}
	return s
}
