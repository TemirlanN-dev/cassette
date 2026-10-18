package report

import (
	"net/http"
	"strings"
	"testing"

	"github.com/TemirlanN-dev/cassette/internal/cassette"
	"github.com/TemirlanN-dev/cassette/internal/match"
)

// claude builds a recording of an Anthropic call. The message ID changes on
// every real recording, so tests use a different one each time.
func claude(prompt, answer, id string, tools ...string) cassette.Interaction {
	content := `{"type":"text","text":"` + answer + `"}`
	for _, t := range tools {
		content += `,{"type":"tool_use","name":"` + t + `","input":{}}`
	}
	return cassette.Interaction{
		Request: cassette.Request{Method: "POST", Path: "/v1/messages",
			Body: cassette.EncodeBody([]byte(`{"messages":[{"role":"user","content":"` + prompt + `"}]}`))},
		Response: cassette.Response{Status: 200, Headers: http.Header{"Content-Type": {"application/json"}},
			Chunks: []cassette.Chunk{{Data: `{"id":"` + id + `","type":"message","content":[` + content + `]}`}}},
	}
}

func runDiff(t *testing.T, before, after []cassette.Interaction) (string, Result) {
	t.Helper()
	m, _ := match.New(match.Config{})
	var out strings.Builder
	res := Diff(&out, before, after, m)
	return out.String(), res
}

func TestDiffUnchangedIgnoresMessageIDs(t *testing.T) {
	out, res := runDiff(t,
		[]cassette.Interaction{claude("Hi", "Hello!", "msg_1")},
		[]cassette.Interaction{claude("Hi", "Hello!", "msg_2")})
	if res.Any() || res.Unchanged != 1 {
		t.Errorf("result = %+v\n%s", res, out)
	}
}

func TestDiffAnswerChanged(t *testing.T) {
	out, res := runDiff(t,
		[]cassette.Interaction{claude("Hi", "Hello!", "msg_1")},
		[]cassette.Interaction{claude("Hi", "Hey there!", "msg_2")})
	if res.Changed != 1 {
		t.Fatalf("result = %+v", res)
	}
	for _, want := range []string{"answer changed", `"Hello!"`, `"Hey there!"`} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
}

func TestDiffEditedPromptAndToolCalls(t *testing.T) {
	out, res := runDiff(t,
		[]cassette.Interaction{claude("Find cats", "Found them.", "msg_1", "search_web")},
		[]cassette.Interaction{claude("Find dogs", "Found them.", "msg_2")})
	if res.Changed != 1 || res.Added != 0 || res.Removed != 0 {
		t.Fatalf("an edited prompt should pair up as one change, got %+v\n%s", res, out)
	}
	for _, want := range []string{
		"probably the same call with an edited prompt",
		`messages.0.content: "Find cats" → "Find dogs"`,
		"search_web({})",
		"(none)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
}

func TestDiffAddedAndRemoved(t *testing.T) {
	added := claude("New", "Yes", "msg_3")
	added.Request.Path = "/v1/other"
	out, res := runDiff(t,
		[]cassette.Interaction{claude("Old", "Bye", "msg_1")},
		[]cassette.Interaction{added})
	if res.Added != 1 || res.Removed != 1 {
		t.Errorf("result = %+v\n%s", res, out)
	}
	if !strings.Contains(out, "+ POST /v1/other") || !strings.Contains(out, "- POST /v1/messages") {
		t.Errorf("output:\n%s", out)
	}
}

func TestDiffDatesDoNotCount(t *testing.T) {
	// The matching rules apply: a different date in the prompt is the same request.
	_, res := runDiff(t,
		[]cassette.Interaction{claude("Today is 2026-10-17", "Hi", "msg_1")},
		[]cassette.Interaction{claude("Today is 2026-10-18", "Hi", "msg_2")})
	if res.Any() {
		t.Errorf("result = %+v", res)
	}
}

func TestDiffUnknownFormatShowsFields(t *testing.T) {
	rec := func(v string) cassette.Interaction {
		return cassette.Interaction{
			Request:  cassette.Request{Method: "POST", Path: "/x", Body: cassette.EncodeBody([]byte(`{}`))},
			Response: cassette.Response{Status: 200, Chunks: []cassette.Chunk{{Data: `{"result":"` + v + `"}`}}},
		}
	}
	out, res := runDiff(t, []cassette.Interaction{rec("a")}, []cassette.Interaction{rec("b")})
	if res.Changed != 1 || !strings.Contains(out, `result: "a" → "b"`) {
		t.Errorf("result = %+v\n%s", res, out)
	}
}

func TestShow(t *testing.T) {
	var out strings.Builder
	Show(&out, &cassette.Cassette{Interactions: []cassette.Interaction{claude("Find cats", "Found them.", "msg_1", "search_web")}})
	for _, want := range []string{"1 recordings", "[1] POST /v1/messages -> 200", `"content": "Find cats"`, "Found them.", "search_web {}"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output is missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "msg_1") {
		t.Error("show should leave out IDs that change on every recording")
	}
}
