package match

import "testing"

func mustNew(t *testing.T, cfg Config) *Matcher {
	t.Helper()
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func boolPtr(b bool) *bool { return &b }

// same reports whether two bodies get the same fingerprint for POST /v1/messages.
func same(m *Matcher, a, b string) bool {
	return m.Fingerprint("POST", "/v1/messages", []byte(a)) == m.Fingerprint("POST", "/v1/messages", []byte(b))
}

func TestKeyOrderDoesNotMatter(t *testing.T) {
	m := mustNew(t, Config{})
	if !same(m, `{"model":"m","max_tokens":10}`, `{"max_tokens":10,"model":"m"}`) {
		t.Error("same JSON with different key order should match")
	}
}

func TestDifferentRequestsDoNotMatch(t *testing.T) {
	m := mustNew(t, Config{})
	if same(m, `{"content":"Hi"}`, `{"content":"Bye"}`) {
		t.Error("different prompts should not match")
	}
	body := []byte(`{"content":"Hi"}`)
	if m.Fingerprint("POST", "/v1/messages", body) == m.Fingerprint("POST", "/v1/complete", body) {
		t.Error("same body on different paths should not match")
	}
	if m.Fingerprint("POST", "/v1/messages", body) == m.Fingerprint("GET", "/v1/messages", body) {
		t.Error("same body with different methods should not match")
	}
}

func TestNumbersKeepTheirExactValue(t *testing.T) {
	m := mustNew(t, Config{})
	// As float64 these two would round to the same value.
	if same(m, `{"seed":12345678901234567890}`, `{"seed":12345678901234567891}`) {
		t.Error("different large numbers should not match")
	}
}

func TestDefaultRules(t *testing.T) {
	m := mustNew(t, Config{})
	cases := []struct{ name, a, b string }{
		{"date", `{"system":"Today is 2026-10-17."}`, `{"system":"Today is 2026-10-18."}`},
		{"timestamp", `{"t":"at 2026-10-17T14:03:22Z"}`, `{"t":"at 2026-10-18T09:00:01.5+02:00"}`},
		{"uuid", `{"session":"550e8400-e29b-41d4-a716-446655440000"}`, `{"session":"7c9e6679-7425-40de-944b-e07fc1f90ae7"}`},
	}
	for _, c := range cases {
		if !same(m, c.a, c.b) {
			t.Errorf("%s: should match with default rules on", c.name)
		}
	}
}

func TestDefaultRulesCanBeTurnedOff(t *testing.T) {
	m := mustNew(t, Config{DefaultRules: boolPtr(false)})
	if same(m, `{"system":"Today is 2026-10-17."}`, `{"system":"Today is 2026-10-18."}`) {
		t.Error("dates should not be ignored when default rules are off")
	}
}

func TestIgnoreField(t *testing.T) {
	m := mustNew(t, Config{Ignore: []string{"metadata.user_id"}})
	if !same(m, `{"metadata":{"user_id":"alice","tier":"pro"}}`, `{"metadata":{"user_id":"bob","tier":"pro"}}`) {
		t.Error("ignored field should not affect matching")
	}
	if same(m, `{"metadata":{"user_id":"alice","tier":"pro"}}`, `{"metadata":{"user_id":"alice","tier":"free"}}`) {
		t.Error("other fields in the same object should still matter")
	}
}

func TestIgnoreFieldInEveryListItem(t *testing.T) {
	m := mustNew(t, Config{Ignore: []string{"messages.*.id"}})
	a := `{"messages":[{"id":"a1","content":"Hi"},{"id":"a2","content":"Yo"}]}`
	b := `{"messages":[{"id":"b1","content":"Hi"},{"id":"b2","content":"Yo"}]}`
	if !same(m, a, b) {
		t.Error("ids in every message should be ignored")
	}
}

func TestIgnoreMissingFieldIsFine(t *testing.T) {
	m := mustNew(t, Config{Ignore: []string{"metadata.user_id", "messages.*.id"}})
	if !same(m, `{"model":"m"}`, `{"model":"m"}`) {
		t.Error("rules for fields that aren't there should do nothing")
	}
}

func TestCustomReplace(t *testing.T) {
	m := mustNew(t, Config{Replace: []Replacement{{Pattern: `C:\\Users\\[^\\]+`, With: "<home>"}}})
	if !same(m, `{"cwd":"C:\\Users\\alice\\app"}`, `{"cwd":"C:\\Users\\bob\\app"}`) {
		t.Error("custom replace rule should make home folders match")
	}
}

func TestNonJSONBody(t *testing.T) {
	m := mustNew(t, Config{})
	if !same(m, "plain text 2026-10-17", "plain text 2026-10-18") {
		t.Error("replace rules should apply to non-JSON bodies too")
	}
	if same(m, "plain text a", "plain text b") {
		t.Error("different non-JSON bodies should not match")
	}
}

func TestBadRules(t *testing.T) {
	if _, err := New(Config{Replace: []Replacement{{Pattern: "(unclosed"}}}); err == nil {
		t.Error("invalid regex should be an error")
	}
	if _, err := New(Config{Ignore: []string{"metadata..id"}}); err == nil {
		t.Error("empty path part should be an error")
	}
}
