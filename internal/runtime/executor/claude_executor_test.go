package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestApplyClaudeToolPrefix(t *testing.T) {
	input := []byte(`{"tools":[{"name":"alpha"},{"name":"proxy_bravo"}],"tool_choice":{"type":"tool","name":"charlie"},"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"delta","id":"t1","input":{}}]}]}`)
	out := applyClaudeToolPrefix(input, "proxy_")

	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "proxy_alpha" {
		t.Fatalf("tools.0.name = %q, want %q", got, "proxy_alpha")
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "proxy_bravo" {
		t.Fatalf("tools.1.name = %q, want %q", got, "proxy_bravo")
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "proxy_charlie" {
		t.Fatalf("tool_choice.name = %q, want %q", got, "proxy_charlie")
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != "proxy_delta" {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, "proxy_delta")
	}
}

func TestEnsureClaudeThinkingDisplay(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		wantDisplay string
		wantExists  bool
	}{
		{
			name:        "enabled defaults to summarized",
			input:       `{"thinking":{"type":"enabled","budget_tokens":2048}}`,
			wantDisplay: "summarized",
			wantExists:  true,
		},
		{
			name:        "adaptive defaults to summarized",
			input:       `{"thinking":{"type":"adaptive"}}`,
			wantDisplay: "summarized",
			wantExists:  true,
		},
		{
			name:        "explicit display preserved",
			input:       `{"thinking":{"type":"enabled","display":"full"}}`,
			wantDisplay: "full",
			wantExists:  true,
		},
		{
			name:       "disabled removes display",
			input:      `{"thinking":{"type":"disabled","display":"summarized"}}`,
			wantExists: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			out := ensureClaudeThinkingDisplay([]byte(tt.input))
			display := gjson.GetBytes(out, "thinking.display")
			if display.Exists() != tt.wantExists {
				t.Fatalf("thinking.display exists = %v, want %v; payload=%s", display.Exists(), tt.wantExists, string(out))
			}
			if tt.wantExists && display.String() != tt.wantDisplay {
				t.Fatalf("thinking.display = %q, want %q; payload=%s", display.String(), tt.wantDisplay, string(out))
			}
		})
	}
}

func TestNormalizeClaudeAssistantThinkingOrder(t *testing.T) {
	t.Parallel()

	input := []byte(`{
		"messages":[
			{"role":"user","content":[{"type":"text","text":"hello"}]},
			{"role":"assistant","content":[
				{"type":"text","text":"before"},
				{"type":"thinking","thinking":"step 1","signature":"sig-1"},
				{"type":"redacted_thinking","data":"abc"},
				{"type":"tool_use","name":"Read","id":"tool_1","input":{}}
			]}
		]
	}`)

	out := normalizeClaudeAssistantThinkingOrder(input)
	content := gjson.GetBytes(out, "messages.1.content").Array()
	if len(content) != 4 {
		t.Fatalf("messages.1.content length = %d, want 4", len(content))
	}
	gotTypes := []string{
		content[0].Get("type").String(),
		content[1].Get("type").String(),
		content[2].Get("type").String(),
		content[3].Get("type").String(),
	}
	wantTypes := []string{"thinking", "redacted_thinking", "text", "tool_use"}
	for i := range wantTypes {
		if gotTypes[i] != wantTypes[i] {
			t.Fatalf("content[%d].type = %q, want %q; payload=%s", i, gotTypes[i], wantTypes[i], string(out))
		}
	}
}

func TestClaudeExecutor_AddsMissingIndependentCacheBreakpoints(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		upstreamBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-opus-4-8","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	payload := []byte(`{
		"model":"claude-opus-4-8",
		"tools":[
			{"name":"tool1","description":"First tool","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}
		],
		"system":"System prompt that should still get cache_control",
		"messages":[
			{"role":"user","content":"First user"},
			{"role":"assistant","content":"Assistant reply"},
			{"role":"user","content":"Second user"}
		]
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-4-8",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gjson.GetBytes(upstreamBody, "tools.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("tool cache_control = %q, want ephemeral", got)
	}
	if got := gjson.GetBytes(upstreamBody, "system.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("system cache_control = %q, want ephemeral", got)
	}
	if got := gjson.GetBytes(upstreamBody, "cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("top-level cache_control = %q, want ephemeral", got)
	}
	if got := gjson.GetBytes(upstreamBody, "messages.0.content.0.cache_control").Exists(); got {
		t.Fatalf("message cache_control should not be injected when using automatic caching; body=%s", string(upstreamBody))
	}
}

func TestClaudeExecutor_FingerprintKeepsSystemCacheOnLastInstruction(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		upstreamBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-opus-4-8","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{
		IdentityFingerprint: config.IdentityFingerprintConfig{
			Claude: config.ClaudeIdentityFingerprintConfig{
				Enabled:     true,
				CLIVersion:  "2.1.88",
				Entrypoint:  "cli",
				SessionMode: "fixed",
				SessionID:   "session-fixed-123",
				DeviceID:    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			},
		},
	})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":  "oauth-access-token",
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"type":         "claude",
			"account_uuid": "account-uuid-123",
		},
	}
	payload := []byte(`{
		"model":"claude-opus-4-8",
		"system":[
			{"type":"text","text":"Project rules"},
			{"type":"text","text":"Repository context"}
		],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"hello"}]}
		]
	}`)

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-4-8",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gjson.GetBytes(upstreamBody, "system.0.cache_control").Exists(); got {
		t.Fatalf("billing header should not carry cache_control; body=%s", string(upstreamBody))
	}
	if got := gjson.GetBytes(upstreamBody, "system.1.cache_control").Exists(); got {
		t.Fatalf("claude prefix should not carry cache_control when later system blocks exist; body=%s", string(upstreamBody))
	}
	if got := gjson.GetBytes(upstreamBody, "system.3.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("last system instruction cache_control = %q, want ephemeral; body=%s", got, string(upstreamBody))
	}
}

func TestDecodeResponseBody_DecodesGzipWithoutHeader(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(`{"ok":true}`)); err != nil {
		t.Fatalf("write gzip payload: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close gzip payload: %v", err)
	}

	decoded, err := decodeResponseBody(io.NopCloser(bytes.NewReader(buf.Bytes())), "")
	if err != nil {
		t.Fatalf("decodeResponseBody returned error: %v", err)
	}
	defer func() { _ = decoded.Close() }()

	body, err := io.ReadAll(decoded)
	if err != nil {
		t.Fatalf("read decoded body: %v", err)
	}
	if got := string(body); got != `{"ok":true}` {
		t.Fatalf("decoded body = %q, want %q", got, `{"ok":true}`)
	}
}

func TestApplyClaudeToolPrefix_WithToolReference(t *testing.T) {
	input := []byte(`{"tools":[{"name":"alpha"}],"messages":[{"role":"user","content":[{"type":"tool_reference","tool_name":"beta"},{"type":"tool_reference","tool_name":"proxy_gamma"}]}]}`)
	out := applyClaudeToolPrefix(input, "proxy_")

	if got := gjson.GetBytes(out, "messages.0.content.0.tool_name").String(); got != "proxy_beta" {
		t.Fatalf("messages.0.content.0.tool_name = %q, want %q", got, "proxy_beta")
	}
	if got := gjson.GetBytes(out, "messages.0.content.1.tool_name").String(); got != "proxy_gamma" {
		t.Fatalf("messages.0.content.1.tool_name = %q, want %q", got, "proxy_gamma")
	}
}

func TestApplyClaudeToolPrefix_SkipsBuiltinTools(t *testing.T) {
	input := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"},{"name":"my_custom_tool","input_schema":{"type":"object"}}]}`)
	out := applyClaudeToolPrefix(input, "proxy_")

	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "web_search" {
		t.Fatalf("built-in tool name should not be prefixed: tools.0.name = %q, want %q", got, "web_search")
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "proxy_my_custom_tool" {
		t.Fatalf("custom tool should be prefixed: tools.1.name = %q, want %q", got, "proxy_my_custom_tool")
	}
}

func TestApplyClaudeToolPrefix_BuiltinToolSkipped(t *testing.T) {
	body := []byte(`{
		"tools": [
			{"type": "web_search_20250305", "name": "web_search", "max_uses": 5},
			{"name": "Read"}
		],
		"messages": [
			{"role": "user", "content": [
				{"type": "tool_use", "name": "web_search", "id": "ws1", "input": {}},
				{"type": "tool_use", "name": "Read", "id": "r1", "input": {}}
			]}
		]
	}`)
	out := applyClaudeToolPrefix(body, "proxy_")

	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "web_search" {
		t.Fatalf("tools.0.name = %q, want %q", got, "web_search")
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != "web_search" {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, "web_search")
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "proxy_Read" {
		t.Fatalf("tools.1.name = %q, want %q", got, "proxy_Read")
	}
	if got := gjson.GetBytes(out, "messages.0.content.1.name").String(); got != "proxy_Read" {
		t.Fatalf("messages.0.content.1.name = %q, want %q", got, "proxy_Read")
	}
}

func TestApplyClaudeToolPrefix_KnownBuiltinInHistoryOnly(t *testing.T) {
	body := []byte(`{
		"tools": [
			{"name": "Read"}
		],
		"messages": [
			{"role": "user", "content": [
				{"type": "tool_use", "name": "web_search", "id": "ws1", "input": {}}
			]}
		]
	}`)
	out := applyClaudeToolPrefix(body, "proxy_")

	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != "web_search" {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, "web_search")
	}
	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "proxy_Read" {
		t.Fatalf("tools.0.name = %q, want %q", got, "proxy_Read")
	}
}

func TestApplyClaudeToolPrefix_CustomToolsPrefixed(t *testing.T) {
	body := []byte(`{
		"tools": [{"name": "Read"}, {"name": "Write"}],
		"messages": [
			{"role": "user", "content": [
				{"type": "tool_use", "name": "Read", "id": "r1", "input": {}},
				{"type": "tool_use", "name": "Write", "id": "w1", "input": {}}
			]}
		]
	}`)
	out := applyClaudeToolPrefix(body, "proxy_")

	if got := gjson.GetBytes(out, "tools.0.name").String(); got != "proxy_Read" {
		t.Fatalf("tools.0.name = %q, want %q", got, "proxy_Read")
	}
	if got := gjson.GetBytes(out, "tools.1.name").String(); got != "proxy_Write" {
		t.Fatalf("tools.1.name = %q, want %q", got, "proxy_Write")
	}
	if got := gjson.GetBytes(out, "messages.0.content.0.name").String(); got != "proxy_Read" {
		t.Fatalf("messages.0.content.0.name = %q, want %q", got, "proxy_Read")
	}
	if got := gjson.GetBytes(out, "messages.0.content.1.name").String(); got != "proxy_Write" {
		t.Fatalf("messages.0.content.1.name = %q, want %q", got, "proxy_Write")
	}
}

func TestApplyClaudeToolPrefix_ToolChoiceBuiltin(t *testing.T) {
	body := []byte(`{
		"tools": [
			{"type": "web_search_20250305", "name": "web_search"},
			{"name": "Read"}
		],
		"tool_choice": {"type": "tool", "name": "web_search"}
	}`)
	out := applyClaudeToolPrefix(body, "proxy_")

	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "web_search" {
		t.Fatalf("tool_choice.name = %q, want %q", got, "web_search")
	}
}

func TestStripClaudeToolPrefixFromResponse(t *testing.T) {
	input := []byte(`{"content":[{"type":"tool_use","name":"proxy_alpha","id":"t1","input":{}},{"type":"tool_use","name":"bravo","id":"t2","input":{}}]}`)
	out := stripClaudeToolPrefixFromResponse(input, "proxy_")

	if got := gjson.GetBytes(out, "content.0.name").String(); got != "alpha" {
		t.Fatalf("content.0.name = %q, want %q", got, "alpha")
	}
	if got := gjson.GetBytes(out, "content.1.name").String(); got != "bravo" {
		t.Fatalf("content.1.name = %q, want %q", got, "bravo")
	}
}

func TestStripClaudeToolPrefixFromResponse_WithToolReference(t *testing.T) {
	input := []byte(`{"content":[{"type":"tool_reference","tool_name":"proxy_alpha"},{"type":"tool_reference","tool_name":"bravo"}]}`)
	out := stripClaudeToolPrefixFromResponse(input, "proxy_")

	if got := gjson.GetBytes(out, "content.0.tool_name").String(); got != "alpha" {
		t.Fatalf("content.0.tool_name = %q, want %q", got, "alpha")
	}
	if got := gjson.GetBytes(out, "content.1.tool_name").String(); got != "bravo" {
		t.Fatalf("content.1.tool_name = %q, want %q", got, "bravo")
	}
}

func TestStripClaudeToolPrefixFromStreamLine(t *testing.T) {
	line := []byte(`data: {"type":"content_block_start","content_block":{"type":"tool_use","name":"proxy_alpha","id":"t1"},"index":0}`)
	out := stripClaudeToolPrefixFromStreamLine(line, "proxy_")

	payload := bytes.TrimSpace(out)
	if bytes.HasPrefix(payload, []byte("data:")) {
		payload = bytes.TrimSpace(payload[len("data:"):])
	}
	if got := gjson.GetBytes(payload, "content_block.name").String(); got != "alpha" {
		t.Fatalf("content_block.name = %q, want %q", got, "alpha")
	}
}

func TestStripClaudeToolPrefixFromStreamLine_WithToolReference(t *testing.T) {
	line := []byte(`data: {"type":"content_block_start","content_block":{"type":"tool_reference","tool_name":"proxy_beta"},"index":0}`)
	out := stripClaudeToolPrefixFromStreamLine(line, "proxy_")

	payload := bytes.TrimSpace(out)
	if bytes.HasPrefix(payload, []byte("data:")) {
		payload = bytes.TrimSpace(payload[len("data:"):])
	}
	if got := gjson.GetBytes(payload, "content_block.tool_name").String(); got != "beta" {
		t.Fatalf("content_block.tool_name = %q, want %q", got, "beta")
	}
}

func TestRehydrateClaudeAssistantThinkingSignatures_UsesCache(t *testing.T) {
	cache.ClearSignatureCache("")
	const modelName = "claude-opus-4-8"
	const thinkingText = "trace this carefully"
	const signature = "sig_123456789012345678901234567890123456789012345678901234567890"
	cache.CacheSignature(modelName, thinkingText, signature)

	input := []byte(`{
		"messages":[
			{"role":"assistant","content":[{"type":"thinking","thinking":"trace this carefully"}]}
		]
	}`)

	out := rehydrateClaudeAssistantThinkingSignatures(input, modelName)
	if got := gjson.GetBytes(out, "messages.0.content.0.signature").String(); got != signature {
		t.Fatalf("messages.0.content.0.signature = %q, want %q", got, signature)
	}
}

func TestSanitizeClaudeDirectResponseForCompatibility_DropsSignatureOnlyThinking(t *testing.T) {
	cache.ClearSignatureCache("")
	const modelName = "claude-opus-4-8"
	input := []byte(`{
		"content":[
			{"type":"thinking","thinking":"","signature":"sig_123456789012345678901234567890123456789012345678901234567890"},
			{"type":"text","text":"hello"}
		]
	}`)

	out := sanitizeClaudeDirectResponseForCompatibility(input, modelName, "opencode/7.3.45 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14")
	if got := len(gjson.GetBytes(out, "content").Array()); got != 1 {
		t.Fatalf("content length = %d, want 1; payload=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "content.0.type").String(); got != "text" {
		t.Fatalf("content.0.type = %q, want %q", got, "text")
	}
}

func TestSanitizeClaudeDirectResponseForCompatibility_StripsAllThinkingForOpencode(t *testing.T) {
	cache.ClearSignatureCache("")
	const modelName = "claude-opus-4-8"
	input := []byte(`{
		"content":[
			{"type":"thinking","thinking":"internal reasoning","signature":"sig_123456789012345678901234567890123456789012345678901234567890"},
			{"type":"text","text":"hello"}
		]
	}`)

	out := sanitizeClaudeDirectResponseForCompatibility(input, modelName, "opencode/7.3.45 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14")
	if got := len(gjson.GetBytes(out, "content").Array()); got != 1 {
		t.Fatalf("content length = %d, want 1; payload=%s", got, string(out))
	}
	if got := gjson.GetBytes(out, "content.0.type").String(); got != "text" {
		t.Fatalf("content.0.type = %q, want text; payload=%s", got, string(out))
	}
	if bytes.Contains(out, []byte("thinking")) {
		t.Fatalf("output still contains thinking block: %s", string(out))
	}
}

func TestClaudeDirectStreamCompat_SuppressesThinkingBlocksForOpencode(t *testing.T) {
	cache.ClearSignatureCache("")
	compat := newClaudeDirectStreamCompat("claude-opus-4-8", "opencode/7.3.45 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14", func(line []byte) []byte {
		return stripClaudeToolPrefixFromStreamLine(line, "proxy_")
	})

	start := []byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\n\n")
	thinkingDelta := []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"internal reasoning\"}}\n\n")
	sig := []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig_123456789012345678901234567890123456789012345678901234567890\"}}\n\n")
	stop := []byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
	tool := []byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"name\":\"proxy_Bash\",\"id\":\"tool_1\"}}\n\n")

	if got := compat.Process(start); len(got) != 0 {
		t.Fatalf("start produced %d events, want 0", len(got))
	}
	if got := compat.Process(thinkingDelta); len(got) != 0 {
		t.Fatalf("thinking delta produced %d events, want 0", len(got))
	}
	if got := compat.Process(sig); len(got) != 0 {
		t.Fatalf("signature produced %d events, want 0", len(got))
	}
	if got := compat.Process(stop); len(got) != 0 {
		t.Fatalf("stop produced %d events, want 0", len(got))
	}

	got := compat.Process(tool)
	if len(got) != 1 {
		t.Fatalf("tool produced %d events, want 1", len(got))
	}
	if bytes.Contains(got[0], []byte("signature_delta")) {
		t.Fatalf("tool event unexpectedly contains signature_delta: %s", string(got[0]))
	}
	if bytes.Contains(got[0], []byte("thinking_delta")) || bytes.Contains(got[0], []byte(`"type":"thinking"`)) {
		t.Fatalf("tool event unexpectedly contains thinking content: %s", string(got[0]))
	}
	if !bytes.Contains(got[0], []byte(`"name":"Bash"`)) {
		t.Fatalf("tool event did not strip tool prefix: %s", string(got[0]))
	}
}

func TestClaudeExecutor_ExecuteStream_StripsSignatureOnlyThinkingForOpencode(t *testing.T) {
	cache.ClearSignatureCache("")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: content_block_start\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"internal reasoning\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_delta\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig_123456789012345678901234567890123456789012345678901234567890\"}}\n\n")
		_, _ = io.WriteString(w, "event: content_block_stop\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		_, _ = io.WriteString(w, "event: content_block_start\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"name\":\"proxy_Bash\",\"id\":\"tool_1\"}}\n\n")
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":  "sk-ant-oat-test",
			"base_url": server.URL,
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("User-Agent", "opencode/7.3.45 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14")
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = req
	ctx := context.WithValue(req.Context(), util.ContextKeyGin, ginCtx)

	stream, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-opus-4-8",
		Payload: []byte(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"adaptive"}}`),
	}, cliproxyexecutor.Options{
		Stream:       true,
		SourceFormat: sdktranslator.FromString("claude"),
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var out bytes.Buffer
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		out.Write(chunk.Payload)
	}

	if bytes.Contains(out.Bytes(), []byte("signature_delta")) {
		t.Fatalf("output still contains signature_delta: %s", out.String())
	}
	if bytes.Contains(out.Bytes(), []byte("thinking_delta")) || bytes.Contains(out.Bytes(), []byte(`"type":"thinking"`)) {
		t.Fatalf("output still contains thinking content: %s", out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte(`"name":"Bash"`)) {
		t.Fatalf("output did not strip tool prefix: %s", out.String())
	}
}

func TestApplyClaudeToolPrefix_NestedToolReference(t *testing.T) {
	input := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_123","content":[{"type":"tool_reference","tool_name":"mcp__nia__manage_resource"}]}]}]}`)
	out := applyClaudeToolPrefix(input, "proxy_")
	got := gjson.GetBytes(out, "messages.0.content.0.content.0.tool_name").String()
	if got != "proxy_mcp__nia__manage_resource" {
		t.Fatalf("nested tool_reference tool_name = %q, want %q", got, "proxy_mcp__nia__manage_resource")
	}
}

func TestClaudeExecutor_ReusesUserIDAcrossModelsWhenCacheEnabled(t *testing.T) {
	resetUserIDCache()

	var userIDs []string
	var requestModels []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		userID := gjson.GetBytes(body, "metadata.user_id").String()
		model := gjson.GetBytes(body, "model").String()
		userIDs = append(userIDs, userID)
		requestModels = append(requestModels, model)
		t.Logf("HTTP Server received request: model=%s, user_id=%s, url=%s", model, userID, r.URL.String())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	t.Logf("End-to-end test: Fake HTTP server started at %s", server.URL)

	cacheEnabled := true
	executor := NewClaudeExecutor(&config.Config{
		ClaudeKey: []config.ClaudeKey{
			{
				APIKey:  "key-123",
				BaseURL: server.URL,
				Cloak: &config.CloakConfig{
					CacheUserID: &cacheEnabled,
				},
			},
		},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	models := []string{"claude-3-5-sonnet", "claude-3-5-haiku"}
	for _, model := range models {
		t.Logf("Sending request for model: %s", model)
		modelPayload, _ := sjson.SetBytes(payload, "model", model)
		if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   model,
			Payload: modelPayload,
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("claude"),
		}); err != nil {
			t.Fatalf("Execute(%s) error: %v", model, err)
		}
	}

	if len(userIDs) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(userIDs))
	}
	if userIDs[0] == "" || userIDs[1] == "" {
		t.Fatal("expected user_id to be populated")
	}
	t.Logf("user_id[0] (model=%s): %s", requestModels[0], userIDs[0])
	t.Logf("user_id[1] (model=%s): %s", requestModels[1], userIDs[1])
	if userIDs[0] != userIDs[1] {
		t.Fatalf("expected user_id to be reused across models, got %q and %q", userIDs[0], userIDs[1])
	}
	if !isValidUserID(userIDs[0]) {
		t.Fatalf("user_id %q is not valid", userIDs[0])
	}
	t.Logf("✓ End-to-end test passed: Same user_id (%s) was used for both models", userIDs[0])
}

func TestClaudeExecutor_GeneratesNewUserIDByDefault(t *testing.T) {
	resetUserIDCache()

	var userIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		userIDs = append(userIDs, gjson.GetBytes(body, "metadata.user_id").String())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-3-5-sonnet","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	for i := 0; i < 2; i++ {
		if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "claude-3-5-sonnet",
			Payload: payload,
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("claude"),
		}); err != nil {
			t.Fatalf("Execute call %d error: %v", i, err)
		}
	}

	if len(userIDs) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(userIDs))
	}
	if userIDs[0] == "" || userIDs[1] == "" {
		t.Fatal("expected user_id to be populated")
	}
	if userIDs[0] == userIDs[1] {
		t.Fatalf("expected user_id to change when caching is not enabled, got identical values %q", userIDs[0])
	}
	if !isValidUserID(userIDs[0]) || !isValidUserID(userIDs[1]) {
		t.Fatalf("user_ids should be valid, got %q and %q", userIDs[0], userIDs[1])
	}
}

func TestClaudeExecutorAppliesClaudeIdentityFingerprint(t *testing.T) {
	var gotHeaders http.Header
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-sonnet-4-5","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewClaudeExecutor(&config.Config{
		IdentityFingerprint: config.IdentityFingerprintConfig{
			Claude: config.ClaudeIdentityFingerprintConfig{
				Enabled:     true,
				CLIVersion:  "2.1.88",
				Entrypoint:  "cli",
				SessionMode: "fixed",
				SessionID:   "session-fixed-123",
				DeviceID:    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			},
		},
	})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":  "oauth-access-token",
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"type":         "claude",
			"account_uuid": "account-uuid-123",
		},
	}
	payload := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":[{"type":"text","text":"hello from user message"}]}]}`)

	if _, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-sonnet-4-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("claude"),
	}); err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if got := gotHeaders.Get("User-Agent"); got != "claude-cli/2.1.88 (external, cli)" {
		t.Fatalf("User-Agent = %q, want Claude Code fingerprint", got)
	}
	if got := gotHeaders.Get("Anthropic-Beta"); !strings.Contains(got, "redact-thinking-2026-02-12") ||
		!strings.Contains(got, "oauth-2025-04-20") {
		t.Fatalf("Anthropic-Beta = %q, want Claude Code OAuth betas", got)
	}
	if got := gotHeaders.Get("X-Stainless-Package-Version"); got != "0.74.0" {
		t.Fatalf("X-Stainless-Package-Version = %q, want 0.74.0", got)
	}
	if got := gotHeaders.Get("X-Stainless-Runtime-Version"); got != "v22.13.0" {
		t.Fatalf("X-Stainless-Runtime-Version = %q, want v22.13.0", got)
	}
	if got := gotHeaders.Get("X-Claude-Code-Session-Id"); got != "session-fixed-123" {
		t.Fatalf("X-Claude-Code-Session-Id = %q, want fixed session", got)
	}
	if got := gotHeaders.Get("X-App"); got != "cli" {
		t.Fatalf("X-App = %q, want cli", got)
	}
	if got := gotHeaders.Get("X-Client-Request-Id"); got == "" {
		t.Fatal("X-Client-Request-Id is empty")
	}

	billing := gjson.GetBytes(gotBody, "system.0.text").String()
	if !strings.Contains(billing, "x-anthropic-billing-header: cc_version=2.1.88.") ||
		!strings.Contains(billing, "cc_entrypoint=cli") {
		t.Fatalf("billing header block = %q, want Claude Code billing fingerprint", billing)
	}
	if got := gjson.GetBytes(gotBody, "system.1.text").String(); got != "You are Claude Code, Anthropic's official CLI for Claude." {
		t.Fatalf("system.1.text = %q, want Claude Code prefix", got)
	}

	var userID struct {
		DeviceID    string `json:"device_id"`
		AccountUUID string `json:"account_uuid"`
		SessionID   string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(gjson.GetBytes(gotBody, "metadata.user_id").String()), &userID); err != nil {
		t.Fatalf("metadata.user_id is not JSON: %v; body=%s", err, string(gotBody))
	}
	if userID.DeviceID != "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" ||
		userID.AccountUUID != "account-uuid-123" || userID.SessionID != "session-fixed-123" {
		t.Fatalf("metadata.user_id = %#v, want device/account/session fingerprint", userID)
	}
}

func TestClaudeBillingHeader_IsStableAcrossUserMessages(t *testing.T) {
	t.Parallel()

	fp := config.ClaudeIdentityFingerprintConfig{
		CLIVersion: "2.1.88",
		Entrypoint: "cli",
	}

	payloadA := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello from the first prompt"}]}]}`)
	payloadB := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"a completely different opener"}]}]}`)

	gotA := buildClaudeBillingHeader(payloadA, fp)
	gotB := buildClaudeBillingHeader(payloadB, fp)

	if gotA != gotB {
		t.Fatalf("billing header changed across user messages:\nA: %s\nB: %s", gotA, gotB)
	}
	if !strings.Contains(gotA, "x-anthropic-billing-header: cc_version=2.1.88.") ||
		!strings.Contains(gotA, "cc_entrypoint=cli") {
		t.Fatalf("billing header = %q, want stable Claude billing fingerprint", gotA)
	}
}

func TestClaudeFingerprintSessionID_ServerStableUsesExecutionAndAffinity(t *testing.T) {
	t.Parallel()

	fp := config.ClaudeIdentityFingerprintConfig{SessionMode: "server-stable"}
	auth := &cliproxyauth.Auth{
		ID: "auth-1",
		Metadata: map[string]any{
			"account_uuid": "account-uuid-123",
		},
	}

	optsA := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "exec-1",
			cliproxyexecutor.AuthAffinityMetadataKey:     "tcb:user-1:key-1",
			cliproxyexecutor.SelectedAuthMetadataKey:     "auth-1",
		},
	}
	optsB := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: "exec-2",
			cliproxyexecutor.AuthAffinityMetadataKey:     "tcb:user-2:key-1",
			cliproxyexecutor.SelectedAuthMetadataKey:     "auth-1",
		},
	}

	got1 := claudeFingerprintSessionID(fp, auth, optsA)
	got2 := claudeFingerprintSessionID(fp, auth, optsA)
	got3 := claudeFingerprintSessionID(fp, auth, optsB)

	if got1 == "" {
		t.Fatal("expected stable session id to be populated")
	}
	if got1 != got2 {
		t.Fatalf("stable session changed for identical execution metadata: %q vs %q", got1, got2)
	}
	if got1 == got3 {
		t.Fatalf("stable session should differ when execution/affinity changes: %q", got1)
	}
}

func TestClaudeFingerprintSessionID_ServerStableFallsBackToGlobalStable(t *testing.T) {
	t.Parallel()

	fp := config.ClaudeIdentityFingerprintConfig{SessionMode: "server-stable"}

	got1 := claudeFingerprintSessionID(fp, nil, cliproxyexecutor.Options{})
	got2 := claudeFingerprintSessionID(fp, nil, cliproxyexecutor.Options{})

	if got1 == "" {
		t.Fatal("expected fallback stable session id to be populated")
	}
	if got1 != got2 {
		t.Fatalf("server-stable fallback changed across calls: %q vs %q", got1, got2)
	}
}

func TestStripClaudeToolPrefixFromResponse_NestedToolReference(t *testing.T) {
	input := []byte(`{"content":[{"type":"tool_result","tool_use_id":"toolu_123","content":[{"type":"tool_reference","tool_name":"proxy_mcp__nia__manage_resource"}]}]}`)
	out := stripClaudeToolPrefixFromResponse(input, "proxy_")
	got := gjson.GetBytes(out, "content.0.content.0.tool_name").String()
	if got != "mcp__nia__manage_resource" {
		t.Fatalf("nested tool_reference tool_name = %q, want %q", got, "mcp__nia__manage_resource")
	}
}

func TestApplyClaudeToolPrefix_NestedToolReferenceWithStringContent(t *testing.T) {
	// tool_result.content can be a string - should not be processed
	input := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_123","content":"plain string result"}]}]}`)
	out := applyClaudeToolPrefix(input, "proxy_")
	got := gjson.GetBytes(out, "messages.0.content.0.content").String()
	if got != "plain string result" {
		t.Fatalf("string content should remain unchanged = %q", got)
	}
}

func TestApplyClaudeToolPrefix_SkipsBuiltinToolReference(t *testing.T) {
	input := []byte(`{"tools":[{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"tool_reference","tool_name":"web_search"}]}]}]}`)
	out := applyClaudeToolPrefix(input, "proxy_")
	got := gjson.GetBytes(out, "messages.0.content.0.content.0.tool_name").String()
	if got != "web_search" {
		t.Fatalf("built-in tool_reference should not be prefixed, got %q", got)
	}
}
