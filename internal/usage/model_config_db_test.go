package usage

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func initModelConfigTestDB(t *testing.T) {
	t.Helper()
	CloseDB()
	dbPath := filepath.Join(t.TempDir(), "usage.db")
	if err := InitDB(dbPath, config.RequestLogStorageConfig{}, time.UTC); err != nil {
		t.Fatalf("InitDB() error = %v", err)
	}
	t.Cleanup(CloseDB)
}

func TestInitDBSeedsDefaultModelConfigs(t *testing.T) {
	initModelConfigTestDB(t)

	models := ListModelConfigs()
	if len(models) == 0 {
		t.Fatal("expected seeded model configs")
	}

	imageModel, ok := GetModelConfig("gpt-image-2")
	if !ok {
		t.Fatal("expected gpt-image-2 to be seeded")
	}
	if imageModel.PricingMode != "call" {
		t.Fatalf("expected gpt-image-2 pricing mode call, got %q", imageModel.PricingMode)
	}
	if imageModel.PricePerCall <= 0 {
		t.Fatalf("expected gpt-image-2 default per-call price, got %v", imageModel.PricePerCall)
	}

	owners := ListModelOwnerPresets()
	if len(owners) == 0 {
		t.Fatal("expected seeded owner presets")
	}
	if _, ok := GetModelOwnerPreset("openai"); !ok {
		t.Fatal("expected openai owner preset")
	}
}

func TestUpsertModelConfigAndPerCallCost(t *testing.T) {
	initModelConfigTestDB(t)

	err := UpsertModelConfig(ModelConfigRow{
		ModelID:      "custom-image",
		OwnedBy:      "acme-ai",
		Description:  "Custom image model",
		Enabled:      true,
		PricingMode:  "call",
		PricePerCall: 0.12,
	})
	if err != nil {
		t.Fatalf("UpsertModelConfig() error = %v", err)
	}

	model, ok := GetModelConfig("custom-image")
	if !ok {
		t.Fatal("expected custom model config")
	}
	if model.OwnedBy != "acme-ai" || model.PricePerCall != 0.12 {
		t.Fatalf("unexpected model config: %+v", model)
	}

	cost := CalculateCost("custom-image", 123, 456, 0)
	if cost != 0.12 {
		t.Fatalf("expected per-call cost 0.12, got %v", cost)
	}
}

func TestDeleteModelConfigRemovesConfigAndPricing(t *testing.T) {
	initModelConfigTestDB(t)

	if err := UpsertModelConfig(ModelConfigRow{
		ModelID:               "temporary-model",
		OwnedBy:               "openai",
		Enabled:               true,
		PricingMode:           "token",
		InputPricePerMillion:  1,
		OutputPricePerMillion: 2,
	}); err != nil {
		t.Fatalf("UpsertModelConfig() error = %v", err)
	}

	if err := DeleteModelConfig("temporary-model"); err != nil {
		t.Fatalf("DeleteModelConfig() error = %v", err)
	}
	if _, ok := GetModelConfig("temporary-model"); ok {
		t.Fatal("expected model config to be deleted")
	}
	if cost := CalculateCost("temporary-model", 1_000_000, 1_000_000, 0); cost != 0 {
		t.Fatalf("expected deleted model cost 0, got %v", cost)
	}
}

func TestAuthGroupModelOwnerMappingLifecycle(t *testing.T) {
	initModelConfigTestDB(t)

	if err := UpsertAuthGroupModelOwnerMapping(AuthGroupModelOwnerMappingRow{
		AuthGroup: "Gemini Canary",
		Owner:     "OpenAI",
	}); err != nil {
		t.Fatalf("UpsertAuthGroupModelOwnerMapping() error = %v", err)
	}

	row, ok := GetAuthGroupModelOwnerMapping("gemini canary")
	if !ok {
		t.Fatal("expected auth-group owner mapping")
	}
	if row.AuthGroup != "gemini-canary" || row.Owner != "openai" {
		t.Fatalf("unexpected auth-group owner mapping: %+v", row)
	}

	rows := ListAuthGroupModelOwnerMappings()
	if len(rows) != 1 {
		t.Fatalf("expected 1 auth-group owner mapping, got %d", len(rows))
	}

	if err := DeleteAuthGroupModelOwnerMapping("gemini canary"); err != nil {
		t.Fatalf("DeleteAuthGroupModelOwnerMapping() error = %v", err)
	}
	if _, ok := GetAuthGroupModelOwnerMapping("gemini-canary"); ok {
		t.Fatal("expected auth-group owner mapping to be deleted")
	}
}

func TestInitDBMergesLegacyPricingWithoutDeadlock(t *testing.T) {
	CloseDB()
	dbPath := filepath.Join(t.TempDir(), "usage.db")

	seedDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	seedDB.SetMaxOpenConns(1)
	if _, err := seedDB.Exec(createPricingTableSQL); err != nil {
		t.Fatalf("create legacy pricing table: %v", err)
	}
	if _, err := seedDB.Exec(
		`INSERT INTO model_pricing (model_id, input_price_per_million, output_price_per_million, cached_price_per_million, updated_at)
		 VALUES ('legacy-model', 1.25, 2.5, 0.5, ?)`,
		time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("insert legacy pricing: %v", err)
	}
	if err := seedDB.Close(); err != nil {
		t.Fatalf("close seed db: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- InitDB(dbPath, config.RequestLogStorageConfig{}, time.UTC)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("InitDB() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("InitDB timed out while merging legacy model pricing")
	}
	t.Cleanup(CloseDB)

	model, ok := GetModelConfig("legacy-model")
	if !ok {
		t.Fatal("expected legacy pricing row to be merged into model_configs")
	}
	if model.PricingMode != "token" || model.InputPricePerMillion != 1.25 || model.OutputPricePerMillion != 2.5 {
		t.Fatalf("unexpected merged model config: %+v", model)
	}
}
