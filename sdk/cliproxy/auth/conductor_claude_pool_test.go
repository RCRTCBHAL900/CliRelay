package auth

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
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

type claudeAffinityFailoverExecutor struct {
	failAuthID string
	calls      []string
}

func (e *claudeAffinityFailoverExecutor) Identifier() string { return "claude" }

func (e *claudeAffinityFailoverExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls = append(e.calls, auth.ID)
	if auth != nil && auth.ID == e.failAuthID {
		return cliproxyexecutor.Response{}, &Error{
			Code:       "upstream_failed",
			Message:    `{"type":"error","error":{"type":"authentication_error","message":"Invalid bearer token"}}`,
			HTTPStatus: http.StatusUnauthorized,
		}
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *claudeAffinityFailoverExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{Code: "not_implemented", Message: "ExecuteStream not implemented", HTTPStatus: http.StatusNotImplemented}
}

func (e *claudeAffinityFailoverExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *claudeAffinityFailoverExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(`{"counted":true}`)}, nil
}

func (e *claudeAffinityFailoverExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{Code: "not_implemented", Message: "HttpRequest not implemented", HTTPStatus: http.StatusNotImplemented}
}

func TestManagerExecute_ClaudeAffinityRemembersSuccessfulMixedFailover(t *testing.T) {
	t.Parallel()

	selector := &RoundRobinSelector{}
	manager := NewManager(nil, selector, nil)
	executor := &claudeAffinityFailoverExecutor{failAuthID: "a"}
	manager.RegisterExecutor(executor)

	auths := []*Auth{
		{ID: "a", Provider: "claude", Status: StatusActive},
		{ID: "b", Provider: "claude", Status: StatusActive},
	}
	for _, auth := range auths {
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, err)
		}
	}
	reg := registry.GetGlobalRegistry()
	for _, auth := range auths {
		reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: "claude-opus-4-8", Created: time.Now().Unix()}})
	}

	affinity := ""
	for i := 0; i < 256; i++ {
		candidate := "tcb:claude-user:" + strconv.Itoa(i)
		picked := pickAffinityAvailable("claude", "claude-opus-4-8", candidate, auths)
		if picked != nil && picked.ID == "a" {
			affinity = candidate
			break
		}
	}
	if affinity == "" {
		t.Fatal("failed to find affinity key that initially selects auth a")
	}

	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.AuthAffinityMetadataKey: affinity,
		},
	}

	if _, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-opus-4-8"}, opts); err != nil {
		t.Fatalf("Execute() first error = %v", err)
	}
	if got := strings.Join(executor.calls, ","); got != "a,b" {
		t.Fatalf("first Execute() calls = %q, want a,b", got)
	}

	if _, err := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: "claude-opus-4-8"}, opts); err != nil {
		t.Fatalf("Execute() second error = %v", err)
	}
	if got := strings.Join(executor.calls, ","); got != "a,b,b" {
		t.Fatalf("second Execute() calls = %q, want a,b,b", got)
	}
}
