// Package inspect reads a recorded response and pulls out what a person cares
// about: the AI's answer and the tools it called. It understands the
// Anthropic, OpenAI and Gemini formats, both normal and streamed.
package inspect

import (
	"bytes"
	"encoding/json"
	"mime"
	"sort"
	"strings"

	"github.com/TemirlanN-dev/cassette/internal/cassette"
)

type ToolCall struct {
	Name  string
	Input string // the arguments, as compact JSON with sorted keys
}

type Summary struct {
	// Provider is "anthropic", "openai" or "gemini", or "" if the format wasn't recognized.
	Provider  string
	Answer    string
	ToolCalls []ToolCall
}

// Body returns the whole response body, with all chunks joined.
func Body(r cassette.Response) string {
	var b strings.Builder
	for _, c := range r.Chunks {
		b.WriteString(c.Data)
	}
	return b.String()
}

// Summarize returns the answer and tool calls in a recorded response.
func Summarize(r cassette.Response) Summary {
	body := Body(r)
	mt, _, _ := mime.ParseMediaType(r.Headers.Get("Content-Type"))
	if mt == "text/event-stream" || strings.HasPrefix(body, "event:") || strings.HasPrefix(body, "data:") {
		return fromStream(body)
	}
	var v map[string]any
	if json.Unmarshal([]byte(body), &v) != nil {
		return Summary{}
	}
	return fromJSON(v)
}

// fromJSON reads a complete (not streamed) response.
func fromJSON(v map[string]any) Summary {
	switch {
	case v["type"] == "message" && v["content"] != nil: // Anthropic Messages
		s := Summary{Provider: "anthropic"}
		for _, block := range list(v["content"]) {
			b := obj(block)
			switch b["type"] {
			case "text":
				s.Answer += str(b["text"])
			case "tool_use":
				s.ToolCalls = append(s.ToolCalls, ToolCall{Name: str(b["name"]), Input: canonical(b["input"])})
			}
		}
		return s

	case v["object"] == "chat.completion": // OpenAI Chat Completions
		s := Summary{Provider: "openai"}
		if choices := list(v["choices"]); len(choices) > 0 {
			msg := obj(obj(choices[0])["message"])
			s.Answer = str(msg["content"])
			for _, tc := range list(msg["tool_calls"]) {
				fn := obj(obj(tc)["function"])
				s.ToolCalls = append(s.ToolCalls, ToolCall{Name: str(fn["name"]), Input: canonicalString(str(fn["arguments"]))})
			}
		}
		return s

	case v["object"] == "response": // OpenAI Responses
		s := Summary{Provider: "openai"}
		for _, item := range list(v["output"]) {
			it := obj(item)
			switch it["type"] {
			case "message":
				for _, part := range list(it["content"]) {
					if p := obj(part); p["type"] == "output_text" {
						s.Answer += str(p["text"])
					}
				}
			case "function_call":
				s.ToolCalls = append(s.ToolCalls, ToolCall{Name: str(it["name"]), Input: canonicalString(str(it["arguments"]))})
			}
		}
		return s

	case v["candidates"] != nil: // Gemini
		s := Summary{Provider: "gemini"}
		addGeminiParts(&s, v)
		return s
	}
	return Summary{}
}

func addGeminiParts(s *Summary, v map[string]any) {
	if cands := list(v["candidates"]); len(cands) > 0 {
		for _, part := range list(obj(obj(cands[0])["content"])["parts"]) {
			p := obj(part)
			s.Answer += str(p["text"])
			if fc := obj(p["functionCall"]); fc != nil {
				s.ToolCalls = append(s.ToolCalls, ToolCall{Name: str(fc["name"]), Input: canonical(fc["args"])})
			}
		}
	}
}

// fromStream rebuilds the answer and tool calls from a streamed response,
// where they arrive a few words (or a few characters of JSON) at a time.
func fromStream(body string) Summary {
	var s Summary
	var answer strings.Builder
	tools := map[int]*ToolCall{} // by position, since pieces of several tool calls can interleave
	var completed *Summary       // OpenAI Responses sends the complete response at the end

	for _, line := range strings.Split(body, "\n") {
		data, ok := strings.CutPrefix(strings.TrimSpace(line), "data:")
		data = strings.TrimSpace(data)
		if !ok || data == "" || data == "[DONE]" {
			continue
		}
		var ev map[string]any
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}

		switch {
		// Anthropic
		case ev["type"] == "message_start":
			s.Provider = "anthropic"
		case ev["type"] == "content_block_start":
			if b := obj(ev["content_block"]); b["type"] == "tool_use" {
				tools[num(ev["index"])] = &ToolCall{Name: str(b["name"])}
			}
		case ev["type"] == "content_block_delta":
			d := obj(ev["delta"])
			switch d["type"] {
			case "text_delta":
				answer.WriteString(str(d["text"]))
			case "input_json_delta":
				if t := tools[num(ev["index"])]; t != nil {
					t.Input += str(d["partial_json"])
				}
			}

		// OpenAI Chat Completions
		case ev["object"] == "chat.completion.chunk":
			s.Provider = "openai"
			if choices := list(ev["choices"]); len(choices) > 0 {
				delta := obj(obj(choices[0])["delta"])
				answer.WriteString(str(delta["content"]))
				for _, tc := range list(delta["tool_calls"]) {
					c := obj(tc)
					i := num(c["index"])
					if tools[i] == nil {
						tools[i] = &ToolCall{}
					}
					fn := obj(c["function"])
					if name := str(fn["name"]); name != "" {
						tools[i].Name = name
					}
					tools[i].Input += str(fn["arguments"])
				}
			}

		// OpenAI Responses
		case ev["type"] == "response.output_text.delta":
			s.Provider = "openai"
			answer.WriteString(str(ev["delta"]))
		case ev["type"] == "response.completed":
			full := fromJSON(obj(ev["response"]))
			completed = &full

		// Gemini
		case ev["candidates"] != nil:
			s.Provider = "gemini"
			piece := Summary{}
			addGeminiParts(&piece, ev)
			answer.WriteString(piece.Answer)
			for _, tc := range piece.ToolCalls {
				tools[len(tools)] = &tc
			}
		}
	}

	if completed != nil && completed.Provider != "" {
		return *completed
	}
	s.Answer = answer.String()
	indexes := make([]int, 0, len(tools))
	for i := range tools {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)
	for _, i := range indexes {
		t := *tools[i]
		t.Input = canonicalString(t.Input)
		s.ToolCalls = append(s.ToolCalls, t)
	}
	return s
}

// canonical turns a JSON value into compact JSON with sorted keys, so two
// tool calls with the same arguments always compare equal.
func canonical(v any) string {
	if v == nil {
		return ""
	}
	out, _ := json.Marshal(v)
	return string(out)
}

// canonicalString does the same for arguments that arrive as a JSON string.
func canonicalString(s string) string {
	var v any
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		return s
	}
	return canonical(v)
}

// Small helpers for walking decoded JSON without a type check at every step.
func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }
func list(v any) []any         { l, _ := v.([]any); return l }
func str(v any) string         { s, _ := v.(string); return s }
func num(v any) int            { f, _ := v.(float64); return int(f) }
