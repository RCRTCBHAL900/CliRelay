package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletionsGroupsSiblingToolCalls(t *testing.T) {
	raw := []byte(`{
		"model":"deepseek-v4-pro",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},
			{"type":"function_call","call_id":"call_1","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"},
			{"type":"function_call","call_id":"call_2","name":"exec_command","arguments":"{\"cmd\":\"ls\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"{\"stdout\":\"/tmp\"}"},
			{"type":"function_call_output","call_id":"call_2","output":"{\"stdout\":\"file\"}"},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}
		]
	}`)

	converted := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-pro", raw, false)

	messages := gjson.GetBytes(converted, "messages").Array()
	if len(messages) != 5 {
		t.Fatalf("expected 5 messages, got %d: %s", len(messages), string(converted))
	}

	if got := messages[1].Get("role").String(); got != "assistant" {
		t.Fatalf("expected grouped assistant tool call message, got role %q", got)
	}

	toolCalls := messages[1].Get("tool_calls").Array()
	if len(toolCalls) != 2 {
		t.Fatalf("expected 2 grouped tool calls, got %d: %s", len(toolCalls), messages[1].Raw)
	}

	if got := toolCalls[0].Get("id").String(); got != "call_1" {
		t.Fatalf("expected first tool call id call_1, got %q", got)
	}
	if got := toolCalls[1].Get("id").String(); got != "call_2" {
		t.Fatalf("expected second tool call id call_2, got %q", got)
	}

	if got := messages[2].Get("role").String(); got != "tool" {
		t.Fatalf("expected first tool response after grouped assistant call, got %q", got)
	}
	if got := messages[2].Get("tool_call_id").String(); got != "call_1" {
		t.Fatalf("expected first tool response for call_1, got %q", got)
	}

	if got := messages[3].Get("role").String(); got != "tool" {
		t.Fatalf("expected second tool response after grouped assistant call, got %q", got)
	}
	if got := messages[3].Get("tool_call_id").String(); got != "call_2" {
		t.Fatalf("expected second tool response for call_2, got %q", got)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletionsAttachesReasoningToAssistant(t *testing.T) {
	raw := []byte(`{
		"model":"deepseek-v4-pro",
		"input":[
			{"type":"reasoning","summary":[{"type":"summary_text","text":"trace this carefully"}],"encrypted_content":""},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Here is the answer"}]}
		]
	}`)

	converted := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-pro", raw, false)

	if got := gjson.GetBytes(converted, "messages.0.reasoning_content").String(); got != "trace this carefully" {
		t.Fatalf("messages.0.reasoning_content = %q, want %q", got, "trace this carefully")
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletionsAttachesFallbackReasoningToToolCalls(t *testing.T) {
	raw := []byte(`{
		"model":"deepseek-v4-pro",
		"input":[
			{"type":"reasoning","summary":[{"type":"summary_text","text":""}],"encrypted_content":"opaque"},
			{"type":"function_call","call_id":"call_1","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"{\"stdout\":\"/tmp\"}"}
		]
	}`)

	converted := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-pro", raw, false)

	if got := gjson.GetBytes(converted, "messages.0.reasoning_content").String(); got != missingReasoningPlaceholder {
		t.Fatalf("messages.0.reasoning_content = %q, want %q", got, missingReasoningPlaceholder)
	}
	if got := len(gjson.GetBytes(converted, "messages.0.tool_calls").Array()); got != 1 {
		t.Fatalf("expected grouped tool call count 1, got %d", got)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletionsMergesAssistantAndFollowingToolCalls(t *testing.T) {
	raw := []byte(`{
		"model":"deepseek-v4-pro",
		"input":[
			{"type":"reasoning","summary":[{"type":"summary_text","text":"check files first"}],"encrypted_content":""},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Let me inspect that."}]},
			{"type":"function_call","call_id":"call_1","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"{\"stdout\":\"/tmp\"}"}
		]
	}`)

	converted := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-pro", raw, false)

	messages := gjson.GetBytes(converted, "messages").Array()
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages after merge, got %d: %s", len(messages), string(converted))
	}
	if got := messages[0].Get("role").String(); got != "assistant" {
		t.Fatalf("messages.0.role = %q, want assistant", got)
	}
	if got := messages[0].Get("reasoning_content").String(); got != "check files first" {
		t.Fatalf("messages.0.reasoning_content = %q, want %q", got, "check files first")
	}
	if got := len(messages[0].Get("tool_calls").Array()); got != 1 {
		t.Fatalf("expected merged assistant tool_calls count 1, got %d", got)
	}
	if got := messages[1].Get("role").String(); got != "tool" {
		t.Fatalf("messages.1.role = %q, want tool", got)
	}
}
