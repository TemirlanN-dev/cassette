package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMissingFileGivesDefaults(t *testing.T) {
	f, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil || f.Match.DefaultRules != nil || len(f.Match.Ignore) != 0 || len(f.Env) != 0 {
		t.Errorf("missing file should give an empty config, got %+v, %v", f, err)
	}
}

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cassette.yaml")
	os.WriteFile(path, []byte(`
default_rules: false
ignore:
  - metadata.user_id
replace:
  - pattern: 'C:\\Users\\[^\\]+'
    with: '<home>'
env:
  MISTRAL_BASE_URL: /v1
`), 0o644)

	f, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	m := f.Match
	if m.DefaultRules == nil || *m.DefaultRules || len(m.Ignore) != 1 || m.Replace[0].With != "<home>" {
		t.Errorf("matching rules = %+v", m)
	}
	if f.Env["MISTRAL_BASE_URL"] != "/v1" {
		t.Errorf("env = %v", f.Env)
	}
}

func TestMisspelledSettingIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cassette.yaml")
	os.WriteFile(path, []byte("ignroe:\n  - x\n"), 0o644)
	if _, err := Load(path); err == nil {
		t.Error("a misspelled setting should be an error")
	}
}
