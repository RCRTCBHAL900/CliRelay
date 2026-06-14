package auth

import (
	"context"
	"testing"
	"time"

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

func TestManagerRegisterAutoAssignsClaudeSourceIPLane(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		ProxyPool: []internalconfig.ProxyPoolEntry{
			{ID: "lane-a", Name: "Lane A", SourceIP: "127.0.0.1", Enabled: true},
			{ID: "lane-b", Name: "Lane B", SourceIP: "127.0.0.2", Enabled: true},
		},
	})

	if _, err := manager.Register(context.Background(), &Auth{
		ID:       "existing",
		Provider: "claude",
		ProxyID:  "lane-a",
		ProxyURL: "sourceip://127.0.0.1",
		Metadata: map[string]any{"proxy_id": "lane-a", "proxy_url": "sourceip://127.0.0.1"},
	}); err != nil {
		t.Fatalf("Register existing: %v", err)
	}

	registered, err := manager.Register(context.Background(), &Auth{
		ID:       "new",
		Provider: "claude",
		Metadata: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Register new: %v", err)
	}

	if registered.ProxyID != "lane-b" {
		t.Fatalf("ProxyID = %q, want lane-b", registered.ProxyID)
	}
	if registered.ProxyURL != "sourceip://127.0.0.2" {
		t.Fatalf("ProxyURL = %q, want sourceip://127.0.0.2", registered.ProxyURL)
	}
	if got, _ := registered.Metadata["proxy_id"].(string); got != "lane-b" {
		t.Fatalf("metadata proxy_id = %#v, want lane-b", registered.Metadata["proxy_id"])
	}
	if got, _ := registered.Metadata["proxy_url"].(string); got != "sourceip://127.0.0.2" {
		t.Fatalf("metadata proxy_url = %#v, want sourceip://127.0.0.2", registered.Metadata["proxy_url"])
	}
}

func TestManagerRegisterImmediatelyQuarantinesExpiredClaudeToken(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	expired := time.Now().Add(-time.Minute).Format(time.RFC3339)

	registered, err := manager.Register(context.Background(), &Auth{
		ID:       "expired",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "tok",
			"refresh_token": "refresh",
			"expired":       expired,
		},
	})
	if err != nil {
		t.Fatalf("Register expired: %v", err)
	}

	if registered.Status != StatusError {
		t.Fatalf("Status = %q, want %q", registered.Status, StatusError)
	}
	if !registered.Unavailable {
		t.Fatal("Unavailable = false, want true")
	}
	if registered.StatusMessage != "refresh pending" {
		t.Fatalf("StatusMessage = %q, want refresh pending", registered.StatusMessage)
	}
	if registered.NextRetryAfter.IsZero() {
		t.Fatal("NextRetryAfter = zero, want future time")
	}
}
