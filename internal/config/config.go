// Package config reads cassette.yaml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/TemirlanN-dev/cassette/internal/match"
)

// File is the contents of cassette.yaml.
type File struct {
	// The matching rules (default_rules, ignore, replace) sit at the top level of the file.
	Match match.Config `yaml:",inline"`

	// Env lists extra environment variables to point at cassette when it runs a
	// test command: variable name -> path to add after cassette's address.
	Env map[string]string `yaml:"env"`
}

// Load reads cassette.yaml. A missing file is fine and means "use the defaults".
func Load(path string) (File, error) {
	var f File
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // a typo like "ignroe:" is an error, not silently ignored
	if err := dec.Decode(&f); err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}
