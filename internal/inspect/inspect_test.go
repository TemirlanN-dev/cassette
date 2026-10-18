package inspect

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/TemirlanN-dev/cassette/internal/cassette"
)

func jsonResponse(body string) cassette.Response {
	return cassette.Response{
		Status:  200,
		Headers: http.Header{"Content-Type": {"application/json"}},
		Chunks:  []cassette.Chunk{{Data: body}},
	}
}

// streamResponse splits events into separate chunks, as a real recording would.
func streamResponse(events ...string) cassette.Response {
	r := cassette.Response{Status: 200, Headers: http.Header{"Content-Type": {"text/event-stream"}}}
	for _, e := range events {
		r.Chunks = append(r.Chunks, cassette.Chunk{Data: e + "\n\n"})
	}
	return r
}

func check(t *testing.T, got Summary, want Summary) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestAnthropicMessage(t *testing.T) {
	got := Summarize(jsonResponse(`{"type":"message","content":[
		{"type":"text","text":"Let me look."},
		{"type":"tool_use","name":"search","input":{"q":"go","limit":3}}]}`))
	check(t, got, Summary{Provider: "anthropic", Answer: "Let me look.",
		ToolCalls: []ToolCall{{Name: "search", Input: `{"limit":3,"q":"go"}`}}})
}

func TestAnthropicStream(t *testing.T) {
	got := Summarize(streamResponse(
		`event: message_start`+"\n"+`data: {"type":"message_start","message":{}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","name":"search"}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"go\"}"}}`,
		`data: {"type":"message_stop"}`,
	))
	check(t, got, Summary{Provider: "anthropic", Answer: "Hello",
		ToolCalls: []ToolCall{{Name: "search", Input: `{"q":"go"}`}}})
}

func TestOpenAIChatCompletion(t *testing.T) {
	got := Summarize(jsonResponse(`{"object":"chat.completion","choices":[{"message":{
		"content":"Hi!","tool_calls":[{"function":{"name":"lookup","arguments":"{\"id\": 7}"}}]}}]}`))
	check(t, got, Summary{Provider: "openai", Answer: "Hi!",
		ToolCalls: []ToolCall{{Name: "lookup", Input: `{"id":7}`}}})
}

func TestOpenAIChatStream(t *testing.T) {
	got := Summarize(streamResponse(
		`data: {"object":"chat.completion.chunk","choices":[{"delta":{"content":"One, "}}]}`,
		`data: {"object":"chat.completion.chunk","choices":[{"delta":{"content":"two."}}]}`,
		`data: {"object":"chat.completion.chunk","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"count","arguments":"{\"to\""}}]}}]}`,
		`data: {"object":"chat.completion.chunk","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":2}"}}]}}]}`,
		`data: [DONE]`,
	))
	check(t, got, Summary{Provider: "openai", Answer: "One, two.",
		ToolCalls: []ToolCall{{Name: "count", Input: `{"to":2}`}}})
}

func TestOpenAIResponses(t *testing.T) {
	full := `{"object":"response","output":[
		{"type":"function_call","name":"weather","arguments":"{\"city\":\"Paris\"}"},
		{"type":"message","content":[{"type":"output_text","text":"Sunny."}]}]}`
	want := Summary{Provider: "openai", Answer: "Sunny.", ToolCalls: []ToolCall{{Name: "weather", Input: `{"city":"Paris"}`}}}

	check(t, Summarize(jsonResponse(full)), want)
	check(t, Summarize(streamResponse(
		`data: {"type":"response.output_text.delta","delta":"Sun"}`,
		`data: {"type":"response.completed","response":`+strings.ReplaceAll(full, "\n", "")+`}`,
	)), want)
}

func TestGemini(t *testing.T) {
	got := Summarize(jsonResponse(`{"candidates":[{"content":{"parts":[
		{"text":"Done."},{"functionCall":{"name":"save","args":{"x":1}}}]}}]}`))
	check(t, got, Summary{Provider: "gemini", Answer: "Done.",
		ToolCalls: []ToolCall{{Name: "save", Input: `{"x":1}`}}})

	stream := Summarize(streamResponse(
		`data: {"candidates":[{"content":{"parts":[{"text":"Hel"}]}}]}`,
		`data: {"candidates":[{"content":{"parts":[{"text":"lo"}]}}]}`,
	))
	check(t, stream, Summary{Provider: "gemini", Answer: "Hello"})
}

func TestUnknownFormat(t *testing.T) {
	check(t, Summarize(jsonResponse(`{"result":"ok"}`)), Summary{})
	check(t, Summarize(jsonResponse(`not json`)), Summary{})
}
