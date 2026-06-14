package auth

import "testing"

func TestDefaultClaudeMaxInFlightLimit_Default(t *testing.T) {
	t.Setenv("CLAUDE_DEFAULT_MAX_INFLIGHT", "")
	if got := defaultClaudeMaxInFlightValue(); got != 2 {
		t.Fatalf("defaultClaudeMaxInFlightValue() = %d, want 2", got)
	}
}

func TestDefaultClaudeMaxInFlightLimit_FromEnv(t *testing.T) {
	t.Setenv("CLAUDE_DEFAULT_MAX_INFLIGHT", "1")
	if got := defaultClaudeMaxInFlightValue(); got != 1 {
		t.Fatalf("defaultClaudeMaxInFlightValue() = %d, want 1", got)
	}
}

func TestDefaultClaudeMaxInFlightLimit_InvalidEnvFallsBack(t *testing.T) {
	t.Setenv("CLAUDE_DEFAULT_MAX_INFLIGHT", "nope")
	if got := defaultClaudeMaxInFlightValue(); got != 2 {
		t.Fatalf("defaultClaudeMaxInFlightValue() = %d, want 2", got)
	}
}
