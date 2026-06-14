package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// --- test helpers ---

// trackingStore records Save and Delete calls.
type trackingStore struct {
	saveCount   atomic.Int32
	deleteCount atomic.Int32
	lastDeleted string
}

func (s *trackingStore) List(context.Context) ([]*Auth, error) { return nil, nil }
func (s *trackingStore) Save(_ context.Context, _ *Auth) (string, error) {
	s.saveCount.Add(1)
	return "", nil
}
func (s *trackingStore) Delete(_ context.Context, id string) error {
	s.deleteCount.Add(1)
	s.lastDeleted = id
	return nil
}

// stubExecutor is a minimal executor whose Refresh result can be controlled.
type stubExecutor struct {
	refreshErr error
	refreshOut *Auth
}

func (e *stubExecutor) Identifier() string { return "test-provider" }
func (e *stubExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *stubExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (e *stubExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	if e.refreshErr != nil {
		return nil, e.refreshErr
	}
	if e.refreshOut != nil {
		return e.refreshOut, nil
	}
	return auth, nil
}
func (e *stubExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *stubExecutor) HttpRequest(_ context.Context, _ *Auth, _ *http.Request) (*http.Response, error) {
	return nil, nil
}

// --- tests ---

func TestRefreshAuth_PermanentError_RemovesCredential(t *testing.T) {
	store := &trackingStore{}
	mgr := NewManager(store, nil, nil)

	auth := &Auth{
		ID:       "test-auth-1",
		Provider: "test-provider",
		Metadata: map[string]any{"type": "codex"},
	}

	// Register the auth and executor.
	ctx := WithSkipPersist(context.Background())
	if _, err := mgr.Register(ctx, auth); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	mgr.RegisterExecutor(&stubExecutor{
		refreshErr: &PermanentAuthError{
			Reason: "credential revoked",
			Cause:  fmt.Errorf("invalid_grant"),
		},
	})

	// Trigger refresh.
	mgr.refreshAuth(context.Background(), "test-auth-1")

	// Auth should be removed from memory.
	if _, ok := mgr.GetByID("test-auth-1"); ok {
		t.Fatal("expected auth to be removed from memory after permanent error")
	}

	// Store.Delete should have been called.
	if got := store.deleteCount.Load(); got != 1 {
		t.Fatalf("expected 1 Delete call, got %d", got)
	}
	if store.lastDeleted != "test-auth-1" {
		t.Fatalf("expected Delete for 'test-auth-1', got '%s'", store.lastDeleted)
	}
}

func TestRefreshAuth_TransientError_KeepsCredential(t *testing.T) {
	store := &trackingStore{}
	mgr := NewManager(store, nil, nil)

	auth := &Auth{
		ID:       "test-auth-2",
		Provider: "test-provider",
		Metadata: map[string]any{"type": "codex"},
	}

	ctx := WithSkipPersist(context.Background())
	if _, err := mgr.Register(ctx, auth); err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	mgr.RegisterExecutor(&stubExecutor{
		refreshErr: fmt.Errorf("network timeout"),
	})

	mgr.refreshAuth(context.Background(), "test-auth-2")

	// Auth should still exist in memory.
	current, ok := mgr.GetByID("test-auth-2")
	if !ok {
		t.Fatal("expected auth to remain in memory after transient error")
	}

	// Should have backoff set.
	if current.NextRefreshAfter.IsZero() {
		t.Fatal("expected NextRefreshAfter to be set after transient error")
	}
	if current.LastError == nil || current.LastError.Message == "" {
		t.Fatal("expected LastError to be set after transient error")
	}

	// Store.Delete should NOT have been called.
	if got := store.deleteCount.Load(); got != 0 {
		t.Fatalf("expected 0 Delete calls, got %d", got)
	}
}

func TestRefreshAuth_Success_UpdatesCredential(t *testing.T) {
	store := &trackingStore{}
	mgr := NewManager(store, nil, nil)

	auth := &Auth{
		ID:             "test-auth-3",
		Provider:       "test-provider",
		Status:         StatusError,
		Unavailable:    true,
		NextRetryAfter: time.Now().Add(10 * time.Minute),
		StatusMessage:  "refresh pending",
		Metadata:       map[string]any{"type": "codex"},
	}

	ctx := WithSkipPersist(context.Background())
	if _, err := mgr.Register(ctx, auth); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	updated := auth.Clone()
	updated.Metadata["access_token"] = "new-token"

	mgr.RegisterExecutor(&stubExecutor{refreshOut: updated})

	mgr.refreshAuth(context.Background(), "test-auth-3")

	// Auth should still exist and be updated.
	current, ok := mgr.GetByID("test-auth-3")
	if !ok {
		t.Fatal("expected auth to remain after successful refresh")
	}
	if current.LastError != nil {
		t.Fatal("expected LastError to be nil after successful refresh")
	}
	if current.Status != StatusActive {
		t.Fatalf("expected StatusActive after successful refresh, got %q", current.Status)
	}
	if current.Unavailable {
		t.Fatal("expected auth to be available after successful refresh")
	}
	if !current.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter to be cleared after successful refresh, got %v", current.NextRetryAfter)
	}
	if !current.LastRefreshedAt.After(time.Time{}) {
		t.Fatal("expected LastRefreshedAt to be set after successful refresh")
	}

	// Store.Delete should NOT have been called.
	if got := store.deleteCount.Load(); got != 0 {
		t.Fatalf("expected 0 Delete calls, got %d", got)
	}

	// Store.Save should have been called (for the Update).
	if got := store.saveCount.Load(); got < 1 {
		t.Fatalf("expected at least 1 Save call for update, got %d", got)
	}
}

func TestHydrateLoadedAuthState_ClaudeExpiredTokenQuarantined(t *testing.T) {
	t.Parallel()

	now := time.Now()
	auth := &Auth{
		ID:       "claude-expired",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "tok",
			"refresh_token": "refresh",
			"expired":       now.Add(-time.Minute).Format(time.RFC3339),
		},
	}

	hydrateLoadedAuthState(auth, now)

	if auth.Status != StatusError {
		t.Fatalf("Status = %q, want %q", auth.Status, StatusError)
	}
	if !auth.Unavailable {
		t.Fatal("Unavailable = false, want true")
	}
	if auth.StatusMessage != "refresh pending" {
		t.Fatalf("StatusMessage = %q, want refresh pending", auth.StatusMessage)
	}
	if auth.NextRetryAfter.IsZero() || auth.NextRetryAfter.Before(now) {
		t.Fatalf("NextRetryAfter = %v, want future time", auth.NextRetryAfter)
	}
}

func TestHydrateLoadedAuthState_ClaudeMissingCredentialsQuarantined(t *testing.T) {
	t.Parallel()

	now := time.Now()
	auth := &Auth{
		ID:       "claude-missing",
		Provider: "claude",
		FileName: "claude-missing.json",
		Status:   StatusActive,
		Metadata: map[string]any{
			"type":  "claude",
			"email": "missing@example.com",
		},
	}

	hydrateLoadedAuthState(auth, now)

	if auth.Status != StatusError {
		t.Fatalf("Status = %q, want %q", auth.Status, StatusError)
	}
	if !auth.Unavailable {
		t.Fatal("Unavailable = false, want true")
	}
	if auth.StatusMessage != "missing credentials" {
		t.Fatalf("StatusMessage = %q, want missing credentials", auth.StatusMessage)
	}
	if auth.NextRetryAfter.IsZero() || auth.NextRetryAfter.Sub(now) < 23*time.Hour {
		t.Fatalf("NextRetryAfter = %v, want long quarantine", auth.NextRetryAfter)
	}
}

func TestIsPermanentAuthError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil",
			err:  nil,
			want: false,
		},
		{
			name: "plain error",
			err:  fmt.Errorf("network timeout"),
			want: false,
		},
		{
			name: "direct PermanentAuthError",
			err:  &PermanentAuthError{Reason: "revoked"},
			want: true,
		},
		{
			name: "wrapped PermanentAuthError",
			err:  fmt.Errorf("refresh failed: %w", &PermanentAuthError{Reason: "expired"}),
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsPermanentAuthError(tt.err); got != tt.want {
				t.Errorf("IsPermanentAuthError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPermanentAuthError_Unwrap(t *testing.T) {
	cause := fmt.Errorf("invalid_grant")
	permanent := &PermanentAuthError{Reason: "revoked", Cause: cause}
	if !errors.Is(permanent, cause) {
		t.Fatal("expected errors.Is to find cause through Unwrap")
	}
}
