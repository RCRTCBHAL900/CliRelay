package management

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/api/bodyutil"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

type recordingPersistAuthStore struct {
	memoryAuthStore
	persistedPaths []string
}

func (s *recordingPersistAuthStore) PersistAuthFiles(ctx context.Context, message string, paths ...string) error {
	_ = ctx
	_ = message
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persistedPaths = append(s.persistedPaths, paths...)
	return nil
}

func TestUploadAuthFileRejectsOversizedMultipart(t *testing.T) {
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	h := &Handler{
		cfg: &config.Config{
			AuthDir: authDir,
		},
		authManager: manager,
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "oversized.json")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	payload := bytes.Repeat([]byte("a"), int(bodyutil.AuthFileBodyLimit)+1)
	if _, err := part.Write(payload); err != nil {
		t.Fatalf("Write payload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close writer: %v", err)
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/auth-files", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c.Request = req

	h.UploadAuthFile(c)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}

	entries, err := os.ReadDir(authDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no files written, got %d", len(entries))
	}
}

func TestUploadAuthFilePersistsUploadedJSONThroughStorePersister(t *testing.T) {
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	store := &recordingPersistAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	h := &Handler{
		cfg: &config.Config{
			AuthDir: authDir,
		},
		authManager: manager,
		tokenStore:  store,
	}

	payload := []byte(`{"type":"codex","email":"subscriber@example.com","subscription_started_at":"2027-01-02T03:04:00Z","subscription_period":"monthly"}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/auth-files?name=codex-subscription.json", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req

	h.UploadAuthFile(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	wantPath := filepath.Join(authDir, "codex-subscription.json")
	store.mu.Lock()
	gotPaths := append([]string(nil), store.persistedPaths...)
	store.mu.Unlock()
	if len(gotPaths) != 1 || gotPaths[0] != wantPath {
		t.Fatalf("persisted paths = %#v, want [%q]", gotPaths, wantPath)
	}
}

func TestRegisterAuthFromFileAppliesRoutingMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	h := &Handler{
		cfg: &config.Config{
			AuthDir: authDir,
		},
		authManager: manager,
	}

	fileName := "claude-pro.json"
	absPath := filepath.Join(authDir, fileName)
	data := []byte(`{"type":"claude","email":"pro@example.com","prefix":"team-a","proxy_url":"http://auth-proxy.local:8080","proxy_id":"premium-egress"}`)
	if err := os.WriteFile(absPath, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := h.registerAuthFromFile(context.Background(), absPath, data); err != nil {
		t.Fatalf("registerAuthFromFile: %v", err)
	}

	auth, ok := manager.GetByID(fileName)
	if !ok || auth == nil {
		t.Fatalf("registered auth not found")
	}
	if auth.Prefix != "team-a" {
		t.Fatalf("Prefix = %q, want team-a", auth.Prefix)
	}
	if auth.ProxyURL != "http://auth-proxy.local:8080" {
		t.Fatalf("ProxyURL = %q, want auth proxy", auth.ProxyURL)
	}
	if auth.ProxyID != "premium-egress" {
		t.Fatalf("ProxyID = %q, want premium-egress", auth.ProxyID)
	}
}

func TestRegisterAuthFromFileAutoAssignsClaudeSourceIPLane(t *testing.T) {
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	h := &Handler{
		cfg: &config.Config{
			AuthDir: authDir,
			ProxyPool: []config.ProxyPoolEntry{
				{ID: "lane-a", Name: "Lane A", SourceIP: "152.89.86.108", Enabled: true},
				{ID: "lane-b", Name: "Lane B", SourceIP: "152.89.86.109", Enabled: true},
			},
		},
		authManager: manager,
	}

	existingPath := filepath.Join(authDir, "existing.json")
	if err := os.WriteFile(existingPath, []byte(`{"type":"claude","email":"existing@example.com","proxy_id":"lane-a"}`), 0o600); err != nil {
		t.Fatalf("WriteFile existing: %v", err)
	}
	if err := h.registerAuthFromFile(context.Background(), existingPath, nil); err != nil {
		t.Fatalf("register existing auth: %v", err)
	}

	newPath := filepath.Join(authDir, "new.json")
	if err := os.WriteFile(newPath, []byte(`{"type":"claude","email":"new@example.com"}`), 0o600); err != nil {
		t.Fatalf("WriteFile new: %v", err)
	}
	if err := h.registerAuthFromFile(context.Background(), newPath, nil); err != nil {
		t.Fatalf("register new auth: %v", err)
	}

	auth, ok := manager.GetByID("new.json")
	if !ok || auth == nil {
		t.Fatalf("registered auth not found")
	}
	if auth.ProxyURL != "sourceip://152.89.86.109" {
		t.Fatalf("ProxyURL = %q, want sourceip://152.89.86.109", auth.ProxyURL)
	}
	if auth.ProxyID != "lane-b" {
		t.Fatalf("ProxyID = %q, want lane-b", auth.ProxyID)
	}

	raw, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("ReadFile new: %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatalf("Unmarshal new auth: %v", err)
	}
	if got, _ := metadata["proxy_url"].(string); got != "sourceip://152.89.86.109" {
		t.Fatalf("persisted proxy_url = %#v, want sourceip://152.89.86.109", metadata["proxy_url"])
	}
	if got, _ := metadata["proxy_id"].(string); got != "lane-b" {
		t.Fatalf("persisted proxy_id = %#v, want lane-b", metadata["proxy_id"])
	}
	if got, _ := metadata["load_aware_scheduler"].(bool); !got {
		t.Fatalf("persisted load_aware_scheduler = %#v, want true", metadata["load_aware_scheduler"])
	}
	if got, ok := metadata["concurrency_limit"].(float64); !ok || int(got) != 1 {
		t.Fatalf("persisted concurrency_limit = %#v, want 1", metadata["concurrency_limit"])
	}
}

func TestRegisterAuthFromFileFallsBackToObservedClaudeLanes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	h := &Handler{
		cfg: &config.Config{
			AuthDir: authDir,
		},
		authManager: manager,
	}

	existingPath := filepath.Join(authDir, "existing.json")
	if err := os.WriteFile(existingPath, []byte(`{"type":"claude","email":"existing@example.com","proxy_url":"sourceip://152.89.86.108","proxy_id":"lane-a"}`), 0o600); err != nil {
		t.Fatalf("WriteFile existing: %v", err)
	}
	if err := h.registerAuthFromFile(context.Background(), existingPath, nil); err != nil {
		t.Fatalf("register existing auth: %v", err)
	}

	newPath := filepath.Join(authDir, "new.json")
	if err := os.WriteFile(newPath, []byte(`{"type":"claude","email":"new@example.com"}`), 0o600); err != nil {
		t.Fatalf("WriteFile new: %v", err)
	}
	if err := h.registerAuthFromFile(context.Background(), newPath, nil); err != nil {
		t.Fatalf("register new auth: %v", err)
	}

	auth, ok := manager.GetByID("new.json")
	if !ok || auth == nil {
		t.Fatalf("registered auth not found")
	}
	if auth.ProxyURL != "sourceip://152.89.86.108" {
		t.Fatalf("ProxyURL = %q, want sourceip://152.89.86.108", auth.ProxyURL)
	}
	if auth.ProxyID != "lane-a" {
		t.Fatalf("ProxyID = %q, want lane-a", auth.ProxyID)
	}
}

func TestSelectAutomaticProxyEntryCountsPendingClaudeOAuthReservations(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previousStore := oauthSessions
	oauthSessions = newOAuthSessionStore(time.Minute)
	t.Cleanup(func() {
		oauthSessions = previousStore
	})

	h := &Handler{
		cfg: &config.Config{
			ProxyPool: []config.ProxyPoolEntry{
				{ID: "lane-a", Name: "Lane A", SourceIP: "152.89.86.108", Enabled: true},
				{ID: "lane-b", Name: "Lane B", SourceIP: "152.89.86.109", Enabled: true},
			},
		},
	}

	RegisterOAuthSessionWithMetadata("pending-claude", "anthropic", map[string]string{
		"proxy_id":  "lane-a",
		"proxy_url": "sourceip://152.89.86.108",
	})

	assigned := h.selectAutomaticProxyEntry("claude", "new-auth")
	if assigned == nil {
		t.Fatal("expected proxy assignment")
	}
	if assigned.ID != "lane-b" {
		t.Fatalf("assigned.ID = %q, want lane-b", assigned.ID)
	}
}

func TestSaveTokenRecordAutoAssignsClaudeSourceIPLane(t *testing.T) {
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	h := &Handler{
		cfg: &config.Config{
			ProxyPool: []config.ProxyPoolEntry{
				{ID: "lane-a", Name: "Lane A", SourceIP: "152.89.86.108", Enabled: true},
				{ID: "lane-b", Name: "Lane B", SourceIP: "152.89.86.109", Enabled: true},
			},
		},
		authManager: manager,
		tokenStore:  store,
	}

	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "existing.json",
		FileName: "existing.json",
		Provider: "claude",
		ProxyID:  "lane-a",
		ProxyURL: "sourceip://152.89.86.108",
		Metadata: map[string]any{
			"type":      "claude",
			"proxy_id":  "lane-a",
			"proxy_url": "sourceip://152.89.86.108",
		},
	}); err != nil {
		t.Fatalf("register existing auth: %v", err)
	}

	record := &coreauth.Auth{
		ID:       "new.json",
		FileName: "new.json",
		Provider: "claude",
		Metadata: map[string]any{
			"type":          "claude",
			"email":         "new@example.com",
			"access_token":  "at-test",
			"refresh_token": "rt-test",
		},
	}

	if _, err := h.saveTokenRecord(context.Background(), record); err != nil {
		t.Fatalf("saveTokenRecord: %v", err)
	}

	store.mu.Lock()
	saved := store.items["new.json"]
	store.mu.Unlock()
	if saved == nil {
		t.Fatalf("saved auth not found")
	}
	if saved.ProxyID != "lane-b" {
		t.Fatalf("ProxyID = %q, want lane-b", saved.ProxyID)
	}
	if saved.ProxyURL != "sourceip://152.89.86.109" {
		t.Fatalf("ProxyURL = %q, want sourceip://152.89.86.109", saved.ProxyURL)
	}
	if got, _ := saved.Metadata["proxy_id"].(string); got != "lane-b" {
		t.Fatalf("metadata proxy_id = %#v, want lane-b", saved.Metadata["proxy_id"])
	}
	if got, _ := saved.Metadata["proxy_url"].(string); got != "sourceip://152.89.86.109" {
		t.Fatalf("metadata proxy_url = %#v, want sourceip://152.89.86.109", saved.Metadata["proxy_url"])
	}
	if got, _ := saved.Metadata["load_aware_scheduler"].(bool); !got {
		t.Fatalf("metadata load_aware_scheduler = %#v, want true", saved.Metadata["load_aware_scheduler"])
	}
	if got, ok := saved.Metadata["concurrency_limit"].(int); !ok || got != 2 {
		t.Fatalf("metadata concurrency_limit = %#v, want 2", saved.Metadata["concurrency_limit"])
	}
}

func TestRegisterAuthFromFilePreservesExplicitClaudeSafetyOverrides(t *testing.T) {
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	h := &Handler{
		cfg: &config.Config{
			AuthDir: authDir,
		},
		authManager: manager,
	}

	authPath := filepath.Join(authDir, "custom.json")
	if err := os.WriteFile(authPath, []byte(`{
		"type":"claude",
		"email":"custom@example.com",
		"cookies":[{"name":"sessionKey","value":"x"}],
		"load_aware_scheduler":false,
		"concurrency_limit":4
	}`), 0o600); err != nil {
		t.Fatalf("WriteFile custom: %v", err)
	}
	if err := h.registerAuthFromFile(context.Background(), authPath, nil); err != nil {
		t.Fatalf("register custom auth: %v", err)
	}

	raw, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile custom: %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatalf("Unmarshal custom auth: %v", err)
	}
	if got, _ := metadata["load_aware_scheduler"].(bool); got {
		t.Fatalf("persisted load_aware_scheduler = %#v, want false override preserved", metadata["load_aware_scheduler"])
	}
	if got, ok := metadata["concurrency_limit"].(float64); !ok || int(got) != 4 {
		t.Fatalf("persisted concurrency_limit = %#v, want 4 override preserved", metadata["concurrency_limit"])
	}
}
func TestListAuthFilesSupportsProviderSearchAndPagination(t *testing.T) {
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	h := &Handler{
		cfg: &config.Config{
			AuthDir: authDir,
		},
		authManager: manager,
	}

	files := map[string]string{
		"alpha.json": `{"type":"claude","email":"alpha@example.com"}`,
		"beta.json":  `{"type":"claude","email":"beta@example.com"}`,
		"gamma.json": `{"type":"codex","email":"gamma@example.com"}`,
	}
	for name, raw := range files {
		path := filepath.Join(authDir, name)
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
		if err := h.registerAuthFromFile(context.Background(), path, nil); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/auth-files?provider=claude&search=beta&limit=1&offset=0", nil)

	h.ListAuthFiles(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Files    []map[string]any `json:"files"`
		Total    int              `json:"total"`
		Offset   int              `json:"offset"`
		Limit    int              `json:"limit"`
		Returned int              `json:"returned"`
		HasMore  bool             `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	if payload.Total != 1 || payload.Returned != 1 || payload.Offset != 0 || payload.Limit != 1 || payload.HasMore {
		t.Fatalf("unexpected pagination payload: %#v", payload)
	}
	if len(payload.Files) != 1 || payload.Files[0]["name"] != "beta.json" {
		t.Fatalf("files = %#v, want beta.json", payload.Files)
	}
}

func TestImportVertexCredentialRejectsOversizedMultipart(t *testing.T) {
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	h := &Handler{
		cfg: &config.Config{
			AuthDir: authDir,
		},
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "vertex.json")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	payload := bytes.Repeat([]byte("a"), int(bodyutil.VertexCredentialBodyLimit)+1)
	if _, err := part.Write(payload); err != nil {
		t.Fatalf("Write payload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close writer: %v", err)
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/vertex/import", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	c.Request = req

	h.ImportVertexCredential(c)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}

	if _, err := os.Stat(filepath.Join(authDir, "vertex.json")); err == nil {
		t.Fatal("unexpected credential file written")
	}
}

func TestRegisterAuthFromFileUsesRelativeIDForRelativeAuthDir(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rootDir := t.TempDir()
	authDirAbs := filepath.Join(rootDir, "auths")
	if err := os.MkdirAll(authDirAbs, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(rootDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(previousWD)
	})

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	h := &Handler{
		cfg: &config.Config{
			AuthDir: "auths",
		},
		authManager: manager,
	}

	fileName := "codex-test.json"
	absPath := filepath.Join(authDirAbs, fileName)
	data := []byte(`{"type":"codex","email":"test@example.com"}`)
	if err := os.WriteFile(absPath, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got := h.authIDForPath(absPath); got != fileName {
		t.Fatalf("authIDForPath(%q) = %q, want %q", absPath, got, fileName)
	}

	watcherID := fileName
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       watcherID,
		FileName: fileName,
		Provider: "codex",
		Label:    "test@example.com",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path": absPath,
		},
		Metadata: map[string]any{
			"type":  "codex",
			"email": "test@example.com",
		},
	}); err != nil {
		t.Fatalf("Register existing auth: %v", err)
	}

	if err := h.registerAuthFromFile(context.Background(), absPath, data); err != nil {
		t.Fatalf("registerAuthFromFile: %v", err)
	}

	auths := manager.List()
	if len(auths) != 1 {
		ids := make([]string, 0, len(auths))
		for _, auth := range auths {
			ids = append(ids, auth.ID)
		}
		t.Fatalf("auth count = %d, want 1 (ids=%v)", len(auths), ids)
	}
	if auths[0].ID != watcherID {
		t.Fatalf("auth id = %q, want %q", auths[0].ID, watcherID)
	}
	if _, ok := manager.GetByID(absPath); ok {
		t.Fatalf("unexpected duplicate auth registered with absolute path id %q", absPath)
	}
}
