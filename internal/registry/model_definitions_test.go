package registry

import "testing"

func TestClaudeStaticModelsIncludeLatestOpusModels(t *testing.T) {
	models := GetStaticModelDefinitionsByChannel("claude")
	modelIDs := make(map[string]bool, len(models))
	for _, model := range models {
		if model != nil {
			modelIDs[model.ID] = true
		}
	}

	for _, id := range []string{"claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6", "claude-sonnet-4-6"} {
		if !modelIDs[id] {
			t.Fatalf("expected claude static models to include %q", id)
		}
		info := LookupStaticModelInfo(id)
		if info == nil {
			t.Fatalf("expected LookupStaticModelInfo to find %q", id)
		}
		if id == "claude-opus-4-8" || id == "claude-opus-4-7" {
			if info.Thinking == nil || !info.Thinking.DynamicAllowed {
				t.Fatalf("expected %q to preserve adaptive thinking support", id)
			}
			if info.ContextLength != 1000000 {
				t.Fatalf("expected %q context length 1000000, got %d", id, info.ContextLength)
			}
		}
	}
}

func TestCodexStaticModelsIncludeCurrentCodexModels(t *testing.T) {
	models := GetStaticModelDefinitionsByChannel("codex")
	modelIDs := make(map[string]bool, len(models))
	for _, model := range models {
		if model != nil {
			modelIDs[model.ID] = true
		}
	}

	for _, id := range []string{"gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex-spark", "gpt-image-2", "codex-auto-review"} {
		if !modelIDs[id] {
			t.Fatalf("expected codex static models to include %q", id)
		}
		if LookupStaticModelInfo(id) == nil {
			t.Fatalf("expected LookupStaticModelInfo to find %q", id)
		}
	}

	if modelIDs["gptimage-2"] {
		t.Fatalf("expected codex static models to exclude removed Cherry alias gptimage-2")
	}
	if LookupStaticModelInfo("gptimage-2") != nil {
		t.Fatalf("expected LookupStaticModelInfo to exclude removed Cherry alias gptimage-2")
	}
}
