package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type claudePoolExecutorStub struct{}

func (e *claudePoolExecutorStub) Identifier() string { return "claude" }

func (e *claudePoolExecutorStub) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *claudePoolExecutorStub) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{Code: "not_implemented", Message: "ExecuteStream not implemented", HTTPStatus: http.StatusNotImplemented}
}

func (e *claudePoolExecutorStub) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *claudePoolExecutorStub) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(`{"counted":true}`)}, nil
}

func (e *claudePoolExecutorStub) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{Code: "not_implemented", Message: "HttpRequest not implemented", HTTPStatus: http.StatusNotImplemented}
}

func TestManagerMarkResult_ClaudeRepeated502EscalatesCooldown(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	if _, err := manager.Register(context.Background(), &Auth{
		ID:       "claude-auth-1",
		Provider: "claude",
		Status:   StatusActive,
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	for i := 0; i < 3; i++ {
		manager.MarkResult(context.Background(), Result{
			AuthID:   "claude-auth-1",
			Provider: "claude",
			Model:    "claude-opus-4-8",
			Success:  false,
			Error: &Error{
				Code:       "upstream_failed",
				Message:    "bad gateway",
				HTTPStatus: http.StatusBadGateway,
			},
		})
	}

	auths := manager.List()
	if len(auths) != 1 {
		t.Fatalf("List() len = %d, want 1", len(auths))
	}
	state := auths[0].ModelStates["claude-opus-4-8"]
	if state == nil {
		t.Fatal("model state missing after repeated failures")
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatal("NextRetryAfter is zero after repeated 502 failures")
	}
	if remaining := time.Until(state.NextRetryAfter); remaining < 4*time.Minute {
		t.Fatalf("cooldown = %v, want at least 4m after repeated 502s", remaining)
	}
}

func TestManagerExecute_ReleasesClaudeInFlightLease(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(&claudePoolExecutorStub{})
	if _, err := manager.Register(context.Background(), &Auth{
		ID:       "claude-auth-2",
		Provider: "claude",
		Status:   StatusActive,
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	resp, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if string(resp.Payload) != `{"ok":true}` {
		t.Fatalf("Execute() payload = %q", string(resp.Payload))
	}

	auths := manager.List()
	if len(auths) != 1 {
		t.Fatalf("List() len = %d, want 1", len(auths))
	}
	if auths[0].CurrentInFlight != 0 {
		t.Fatalf("CurrentInFlight = %d, want 0 after request completes", auths[0].CurrentInFlight)
	}
}
