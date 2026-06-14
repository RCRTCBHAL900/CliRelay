package auth

import "testing"

func TestDefaultClaudeMaxInFlightLimit_Default(t *testing.T) {
	t.Setenv("CLAUDE_DEFAULT_MAX_INFLIGHT", "")
	if got := defaultClaudeMaxInFlightLimit(); got != defaultClaudeMaxInFlight {
		t.Fatalf("defaultClaudeMaxInFlightLimit() = %d, want %d", got, defaultClaudeMaxInFlight)
	}
}

func TestDefaultClaudeMaxInFlightLimit_FromEnv(t *testing.T) {
	t.Setenv("CLAUDE_DEFAULT_MAX_INFLIGHT", "1")
	if got := defaultClaudeMaxInFlightLimit(); got != 1 {
		t.Fatalf("defaultClaudeMaxInFlightLimit() = %d, want 1", got)
	}
}

func TestDefaultClaudeMaxInFlightLimit_InvalidEnvFallsBack(t *testing.T) {
	t.Setenv("CLAUDE_DEFAULT_MAX_INFLIGHT", "nope")
	if got := defaultClaudeMaxInFlightLimit(); got != defaultClaudeMaxInFlight {
		t.Fatalf("defaultClaudeMaxInFlightLimit() = %d, want %d", got, defaultClaudeMaxInFlight)
	}
}
