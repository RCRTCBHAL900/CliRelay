package auth

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestNormalizeLoadedClaudeProxyAssignmentsAssignsMissingClaudeLanes(t *testing.T) {
	t.Parallel()

	cfg := &internalconfig.Config{
		ProxyPool: []internalconfig.ProxyPoolEntry{
			{ID: "lane-a", Name: "Lane A", SourceIP: "127.0.0.1", Enabled: true},
			{ID: "lane-b", Name: "Lane B", SourceIP: "127.0.0.2", Enabled: true},
		},
	}
	items := []*Auth{
		{ID: "existing", Provider: "claude", ProxyID: "lane-a", ProxyURL: "sourceip://127.0.0.1", Metadata: map[string]any{}},
		{ID: "missing-1", Provider: "claude", Metadata: map[string]any{}},
		{ID: "missing-2", Provider: "claude", Metadata: map[string]any{}},
		{ID: "gemini", Provider: "gemini", Metadata: map[string]any{}},
	}

	updated := normalizeLoadedClaudeProxyAssignments(cfg, items)
	if len(updated) != 2 {
		t.Fatalf("updated len = %d, want 2", len(updated))
	}

	if items[1].ProxyID != "lane-b" || items[1].ProxyURL != "sourceip://127.0.0.2" {
		t.Fatalf("missing-1 assignment = (%q, %q)", items[1].ProxyID, items[1].ProxyURL)
	}
	if items[2].ProxyID != "lane-a" || items[2].ProxyURL != "sourceip://127.0.0.1" {
		t.Fatalf("missing-2 assignment = (%q, %q)", items[2].ProxyID, items[2].ProxyURL)
	}
	if got := items[1].Metadata["proxy_id"]; got != "lane-b" {
		t.Fatalf("missing-1 metadata proxy_id = %v", got)
	}
	if got := items[2].Metadata["proxy_url"]; got != "sourceip://127.0.0.1" {
		t.Fatalf("missing-2 metadata proxy_url = %v", got)
	}
	if items[3].ProxyID != "" || items[3].ProxyURL != "" {
		t.Fatalf("non-claude assignment changed: %#v", items[3])
	}
}

func TestNormalizeLoadedClaudeProxyAssignmentsSkipsWhenPoolEmpty(t *testing.T) {
	t.Parallel()

	items := []*Auth{
		{ID: "missing", Provider: "claude", Metadata: map[string]any{}},
	}
	updated := normalizeLoadedClaudeProxyAssignments(&internalconfig.Config{}, items)
	if len(updated) != 0 {
		t.Fatalf("updated len = %d, want 0", len(updated))
	}
	if items[0].ProxyID != "" || items[0].ProxyURL != "" {
		t.Fatalf("unexpected assignment: %#v", items[0])
	}
}
