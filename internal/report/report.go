// Package report turns cassettes into text for people: `cassette show` prints
// one cassette readably, and `cassette diff` explains what changed between two.
package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"github.com/TemirlanN-dev/cassette/internal/cassette"
	"github.com/TemirlanN-dev/cassette/internal/inspect"
	"github.com/TemirlanN-dev/cassette/internal/match"
)

// Show prints every recording in c: the request, and the answer and tool calls
// in the response. The output never changes unless the recordings do, so Git
// can use it to show readable diffs of cassette files.
func Show(w io.Writer, c *cassette.Cassette) {
	fmt.Fprintf(w, "%d recordings\n", len(c.Interactions))
	for n, i := range c.Interactions {
		fmt.Fprintf(w, "\n[%d] %s %s -> %d\n", n+1, i.Request.Method, i.Request.Path, i.Response.Status)
		if len(i.Request.Body) > 0 {
			fmt.Fprintln(w, "request:")
			fmt.Fprintln(w, indent(prettyJSON(i.Request.Body), "  "))
		}
		s := inspect.Summarize(i.Response)
		if s.Provider == "" {
			fmt.Fprintln(w, "response:")
			fmt.Fprintln(w, indent(prettyJSON([]byte(inspect.Body(i.Response))), "  "))
			continue
		}
		fmt.Fprintln(w, "answer:")
		fmt.Fprintln(w, indent(s.Answer, "  "))
		if len(s.ToolCalls) > 0 {
			fmt.Fprintln(w, "tool calls:")
			for _, tc := range s.ToolCalls {
				fmt.Fprintf(w, "  %s %s\n", tc.Name, tc.Input)
			}
		}
	}
}

// Result counts what Diff found.
type Result struct {
	Changed, Added, Removed, Unchanged int
}

func (r Result) Any() bool { return r.Changed+r.Added+r.Removed > 0 }

type entry struct {
	i      cassette.Interaction
	fp     string
	paired bool
}

// Diff explains how the recordings in after differ from those in before.
//
// Recordings are paired up in two passes. First, identical requests (same
// fingerprint) are paired. Then the leftovers are paired in order if they go
// to the same method and path: those are most likely the same call with an
// edited prompt. Anything left after that was added or removed.
func Diff(w io.Writer, before, after []cassette.Interaction, m *match.Matcher) Result {
	old := entries(before, m)
	cur := entries(after, m)
	var res Result

	for _, n := range cur {
		for _, o := range old {
			if !o.paired && o.fp == n.fp {
				o.paired, n.paired = true, true
				if sameResponse(o.i, n.i, m) {
					res.Unchanged++
				} else {
					res.Changed++
					fmt.Fprintf(w, "~ %s %s   answer changed\n", n.i.Request.Method, n.i.Request.Path)
					writeResponseDiff(w, o.i, n.i, m)
					fmt.Fprintln(w)
				}
				break
			}
		}
	}

	for _, n := range cur {
		if n.paired {
			continue
		}
		for _, o := range old {
			if !o.paired && o.i.Request.Method == n.i.Request.Method && o.i.Request.Path == n.i.Request.Path {
				o.paired, n.paired = true, true
				res.Changed++
				fmt.Fprintf(w, "~ %s %s   request changed (probably the same call with an edited prompt)\n", n.i.Request.Method, n.i.Request.Path)
				writeRequestDiff(w, o.i, n.i, m)
				if !sameResponse(o.i, n.i, m) {
					writeResponseDiff(w, o.i, n.i, m)
				} else {
					fmt.Fprintln(w, "    answer: unchanged")
				}
				fmt.Fprintln(w)
				break
			}
		}
	}

	for _, n := range cur {
		if !n.paired {
			res.Added++
			fmt.Fprintf(w, "+ %s %s   new recording\n", n.i.Request.Method, n.i.Request.Path)
			writeSummary(w, n.i)
			fmt.Fprintln(w)
		}
	}
	for _, o := range old {
		if !o.paired {
			res.Removed++
			fmt.Fprintf(w, "- %s %s   no longer recorded\n", o.i.Request.Method, o.i.Request.Path)
			writeSummary(w, o.i)
			fmt.Fprintln(w)
		}
	}

	fmt.Fprintf(w, "%d changed, %d added, %d removed, %d unchanged\n", res.Changed, res.Added, res.Removed, res.Unchanged)
	return res
}

func entries(list []cassette.Interaction, m *match.Matcher) []*entry {
	out := make([]*entry, len(list))
	for k, i := range list {
		out[k] = &entry{i: i, fp: m.Fingerprint(i.Request.Method, i.Request.Path, i.Request.Body)}
	}
	return out
}

// sameResponse compares what matters: the answer and tool calls when the format
// is known, or the body with the matching rules applied otherwise. Things that
// change on every recording, like message IDs, don't count.
func sameResponse(a, b cassette.Interaction, m *match.Matcher) bool {
	sa, sb := inspect.Summarize(a.Response), inspect.Summarize(b.Response)
	if sa.Provider != "" && sa.Provider == sb.Provider {
		return sa.Answer == sb.Answer && toolsString(sa.ToolCalls) == toolsString(sb.ToolCalls)
	}
	return a.Response.Status == b.Response.Status &&
		bytes.Equal(m.Normalize([]byte(inspect.Body(a.Response))), m.Normalize([]byte(inspect.Body(b.Response))))
}

func writeRequestDiff(w io.Writer, a, b cassette.Interaction, m *match.Matcher) {
	lines := jsonDiff(m.Normalize(a.Request.Body), m.Normalize(b.Request.Body))
	fmt.Fprintln(w, "    request:")
	writeLimited(w, lines, "      ")
}

func writeResponseDiff(w io.Writer, a, b cassette.Interaction, m *match.Matcher) {
	sa, sb := inspect.Summarize(a.Response), inspect.Summarize(b.Response)
	if sa.Provider == "" || sa.Provider != sb.Provider {
		if a.Response.Status != b.Response.Status {
			fmt.Fprintf(w, "    status: %d → %d\n", a.Response.Status, b.Response.Status)
		}
		fmt.Fprintln(w, "    response:")
		writeLimited(w, jsonDiff(m.Normalize([]byte(inspect.Body(a.Response))), m.Normalize([]byte(inspect.Body(b.Response)))), "      ")
		return
	}
	if sa.Answer != sb.Answer {
		fmt.Fprintf(w, "    answer:      %s\n", quote(sa.Answer))
		fmt.Fprintf(w, "             →   %s\n", quote(sb.Answer))
	}
	if toolsString(sa.ToolCalls) != toolsString(sb.ToolCalls) {
		fmt.Fprintf(w, "    tool calls:  %s\n", toolNames(sa.ToolCalls))
		fmt.Fprintf(w, "             →   %s\n", toolNames(sb.ToolCalls))
	}
}

func writeSummary(w io.Writer, i cassette.Interaction) {
	s := inspect.Summarize(i.Response)
	if s.Provider == "" {
		fmt.Fprintf(w, "    response: %s\n", quote(inspect.Body(i.Response)))
		return
	}
	fmt.Fprintf(w, "    answer: %s\n", quote(s.Answer))
	if len(s.ToolCalls) > 0 {
		fmt.Fprintf(w, "    tool calls: %s\n", toolNames(s.ToolCalls))
	}
}

// jsonDiff lists the fields that differ between two JSON documents, like
// `messages.0.content: "Say hello" → "Say hi"`.
func jsonDiff(a, b []byte) []string {
	var va, vb any
	if json.Unmarshal(a, &va) != nil || json.Unmarshal(b, &vb) != nil {
		if bytes.Equal(a, b) {
			return nil
		}
		return []string{quote(string(a)) + " → " + quote(string(b))}
	}
	var out []string
	diffValues("", va, vb, &out)
	return out
}

func diffValues(path string, a, b any, out *[]string) {
	name := path
	if name == "" {
		name = "(whole body)"
	}
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			break
		}
		keys := map[string]bool{}
		for k := range av {
			keys[k] = true
		}
		for k := range bv {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			x, inA := av[k]
			y, inB := bv[k]
			p := join(path, k)
			switch {
			case !inA:
				*out = append(*out, fmt.Sprintf("+ %s: %s", p, short(y)))
			case !inB:
				*out = append(*out, fmt.Sprintf("- %s: %s", p, short(x)))
			default:
				diffValues(p, x, y, out)
			}
		}
		return
	case []any:
		bv, ok := b.([]any)
		if !ok {
			break
		}
		for k := 0; k < len(av) || k < len(bv); k++ {
			p := join(path, fmt.Sprint(k))
			switch {
			case k >= len(av):
				*out = append(*out, fmt.Sprintf("+ %s: %s", p, short(bv[k])))
			case k >= len(bv):
				*out = append(*out, fmt.Sprintf("- %s: %s", p, short(av[k])))
			default:
				diffValues(p, av[k], bv[k], out)
			}
		}
		return
	}
	if !reflect.DeepEqual(a, b) {
		*out = append(*out, fmt.Sprintf("%s: %s → %s", name, short(a), short(b)))
	}
}

// At most this many differences are listed per recording, so one huge change
// doesn't bury the rest.
const maxLines = 15

func writeLimited(w io.Writer, lines []string, prefix string) {
	if len(lines) == 0 {
		fmt.Fprintln(w, prefix+"(only values the matching rules ignore changed)")
		return
	}
	for k, l := range lines {
		if k == maxLines {
			fmt.Fprintf(w, "%s…and %d more\n", prefix, len(lines)-maxLines)
			return
		}
		fmt.Fprintln(w, prefix+l)
	}
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func short(v any) string {
	if s, ok := v.(string); ok {
		return quote(s)
	}
	out, _ := json.Marshal(v)
	return truncate(string(out), 80)
}

func quote(s string) string {
	s = strings.Join(strings.Fields(s), " ") // put multi-line text on one line
	return `"` + truncate(s, 100) + `"`
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func toolsString(calls []inspect.ToolCall) string {
	var b strings.Builder
	for _, c := range calls {
		b.WriteString(c.Name + " " + c.Input + "\n")
	}
	return b.String()
}

func toolNames(calls []inspect.ToolCall) string {
	if len(calls) == 0 {
		return "(none)"
	}
	names := make([]string, len(calls))
	for k, c := range calls {
		names[k] = c.Name + "(" + truncate(c.Input, 40) + ")"
	}
	return strings.Join(names, ", then ")
}

func prettyJSON(b []byte) string {
	var out bytes.Buffer
	if json.Indent(&out, bytes.TrimSpace(b), "", "  ") != nil {
		var s string
		if json.Unmarshal(b, &s) == nil {
			return s // a non-JSON body, stored as a JSON string
		}
		return string(b)
	}
	return out.String()
}

func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n"+prefix)
}
