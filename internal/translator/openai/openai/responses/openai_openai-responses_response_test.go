package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponsesStreamsMultipleToolCallsSeparately(t *testing.T) {
	raw := []byte(`{
		"id":"chatcmpl_test",
		"object":"chat.completion.chunk",
		"created":123,
		"choices":[
			{
				"index":0,
				"delta":{
					"tool_calls":[
						{"index":0,"id":"call_1","type":"function","function":{"name":"exec_command","arguments":"{\"cmd\":\"pwd\"}"}},
						{"index":1,"id":"call_2","type":"function","function":{"name":"exec_command","arguments":"{\"cmd\":\"ls\"}"}}
					]
				},
				"finish_reason":"tool_calls"
			}
		]
	}`)

	var param any
	events := ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "deepseek-v4-pro", nil, nil, raw, &param)
	all := strings.Join(events, "\n")

	if strings.Count(all, `"item_id":"fc_call_1"`) == 0 {
		t.Fatalf("expected separate function_call_arguments events for call_1, got: %s", all)
	}
	if strings.Count(all, `"item_id":"fc_call_2"`) == 0 {
		t.Fatalf("expected separate function_call_arguments events for call_2, got: %s", all)
	}
	if strings.Contains(all, `{"cmd":"pwd"}{"cmd":"ls"}`) {
		t.Fatalf("unexpected concatenated arguments in stream output: %s", all)
	}

	var completed string
	for _, event := range events {
		if strings.HasPrefix(event, "event: response.completed\n") {
			completed = strings.TrimPrefix(event, "event: response.completed\ndata: ")
		}
	}
	if completed == "" {
		t.Fatalf("missing response.completed event: %v", events)
	}

	output := gjson.Parse(completed).Get("response.output").Array()
	if len(output) != 2 {
		t.Fatalf("expected 2 function_call outputs, got %d: %s", len(output), completed)
	}
	if got := output[0].Get("call_id").String(); got != "call_1" {
		t.Fatalf("first call_id = %q, want %q", got, "call_1")
	}
	if got := output[0].Get("arguments").String(); got != `{"cmd":"pwd"}` {
		t.Fatalf("first arguments = %q, want %q", got, `{"cmd":"pwd"}`)
	}
	if got := output[1].Get("call_id").String(); got != "call_2" {
		t.Fatalf("second call_id = %q, want %q", got, "call_2")
	}
	if got := output[1].Get("arguments").String(); got != `{"cmd":"ls"}` {
		t.Fatalf("second arguments = %q, want %q", got, `{"cmd":"ls"}`)
	}
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponsesHandlesFullMessageStreamingChunk(t *testing.T) {
	raw := []byte(`{
		"id":"chatcmpl_test",
		"object":"chat.completion.chunk",
		"created":123,
		"choices":[
			{
				"index":0,
				"message":{
					"role":"assistant",
					"content":"Updated the script.",
					"reasoning_content":"wrap up cleanly"
				},
				"finish_reason":"stop"
			}
		]
	}`)

	var param any
	events := ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "deepseek-v4-pro", nil, nil, raw, &param)
	all := strings.Join(events, "\n")

	if !strings.Contains(all, `"delta":"Updated the script."`) {
		t.Fatalf("expected output_text delta for full message chunk, got: %s", all)
	}
	if !strings.Contains(all, `"delta":"wrap up cleanly"`) {
		t.Fatalf("expected reasoning delta for full message chunk, got: %s", all)
	}

	var completed string
	for _, event := range events {
		if strings.HasPrefix(event, "event: response.completed\n") {
			completed = strings.TrimPrefix(event, "event: response.completed\ndata: ")
		}
	}
	if completed == "" {
		t.Fatalf("missing response.completed event: %v", events)
	}

	output := gjson.Parse(completed).Get("response.output").Array()
	if len(output) != 2 {
		t.Fatalf("expected reasoning + message outputs, got %d: %s", len(output), completed)
	}
	if got := output[0].Get("type").String(); got != "reasoning" {
		t.Fatalf("output[0].type = %q, want reasoning", got)
	}
	if got := output[0].Get("summary.0.text").String(); got != "wrap up cleanly" {
		t.Fatalf("reasoning summary = %q, want %q", got, "wrap up cleanly")
	}
	if got := output[1].Get("type").String(); got != "message" {
		t.Fatalf("output[1].type = %q, want message", got)
	}
	if got := output[1].Get("content.0.text").String(); got != "Updated the script." {
		t.Fatalf("message text = %q, want %q", got, "Updated the script.")
	}
}
