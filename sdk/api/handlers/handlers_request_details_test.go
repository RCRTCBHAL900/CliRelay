package handlers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	internalrouting "github.com/router-for-me/CLIProxyAPI/v6/internal/routing"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestGetRequestDetails_PreservesSuffix(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	now := time.Now().Unix()

	modelRegistry.RegisterClient("test-request-details-gemini", "gemini", []*registry.ModelInfo{
		{ID: "gemini-2.5-pro", Created: now + 30},
		{ID: "gemini-2.5-flash", Created: now + 25},
	})
	modelRegistry.RegisterClient("test-request-details-openai", "openai", []*registry.ModelInfo{
		{ID: "gpt-5.2", Created: now + 20},
	})
	modelRegistry.RegisterClient("test-request-details-claude", "claude", []*registry.ModelInfo{
		{ID: "claude-sonnet-4-5", Created: now + 5},
	})

	// Ensure cleanup of all test registrations.
	clientIDs := []string{
		"test-request-details-gemini",
		"test-request-details-openai",
		"test-request-details-claude",
	}
	for _, clientID := range clientIDs {
		id := clientID
		t.Cleanup(func() {
			modelRegistry.UnregisterClient(id)
		})
	}

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))

	tests := []struct {
		name          string
		inputModel    string
		wantProviders []string
		wantModel     string
		wantErr       bool
	}{
		{
			name:          "numeric suffix preserved",
			inputModel:    "gemini-2.5-pro(8192)",
			wantProviders: []string{"gemini"},
			wantModel:     "gemini-2.5-pro(8192)",
			wantErr:       false,
		},
		{
			name:          "level suffix preserved",
			inputModel:    "gpt-5.2(high)",
			wantProviders: []string{"openai"},
			wantModel:     "gpt-5.2(high)",
			wantErr:       false,
		},
		{
			name:          "no suffix unchanged",
			inputModel:    "claude-sonnet-4-5",
			wantProviders: []string{"claude"},
			wantModel:     "claude-sonnet-4-5",
			wantErr:       false,
		},
		{
			name:          "unknown model with suffix",
			inputModel:    "unknown-model(8192)",
			wantProviders: nil,
			wantModel:     "",
			wantErr:       true,
		},
		{
			name:          "auto suffix resolved",
			inputModel:    "auto(high)",
			wantProviders: []string{"gemini"},
			wantModel:     "gemini-2.5-pro(high)",
			wantErr:       false,
		},
		{
			name:          "special suffix none preserved",
			inputModel:    "gemini-2.5-flash(none)",
			wantProviders: []string{"gemini"},
			wantModel:     "gemini-2.5-flash(none)",
			wantErr:       false,
		},
		{
			name:          "special suffix auto preserved",
			inputModel:    "claude-sonnet-4-5(auto)",
			wantProviders: []string{"claude"},
			wantModel:     "claude-sonnet-4-5(auto)",
			wantErr:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			providers, model, errMsg := handler.getRequestDetails(context.Background(), tt.inputModel)
			if (errMsg != nil) != tt.wantErr {
				t.Fatalf("getRequestDetails() error = %v, wantErr %v", errMsg, tt.wantErr)
			}
			if errMsg != nil {
				return
			}
			if !reflect.DeepEqual(providers, tt.wantProviders) {
				t.Fatalf("getRequestDetails() providers = %v, want %v", providers, tt.wantProviders)
			}
			if model != tt.wantModel {
				t.Fatalf("getRequestDetails() model = %v, want %v", model, tt.wantModel)
			}
		})
	}
}

func TestGetRequestDetails_AllowedGroupResolvesPrefixedModelProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)

	modelRegistry := registry.GetGlobalRegistry()
	now := time.Now().Unix()
	modelRegistry.RegisterClient("test-request-details-pro", "openai", []*registry.ModelInfo{
		{ID: "pro/gpt-5", Created: now},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient("test-request-details-pro")
	})

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Set("accessMetadata", map[string]string{"allowed-channel-groups": "pro"})
	ctx := context.WithValue(context.Background(), util.ContextKeyGin, ginCtx)

	providers, model, errMsg := handler.getRequestDetails(ctx, "gpt-5")
	if errMsg != nil {
		t.Fatalf("getRequestDetails() unexpected error = %v", errMsg)
	}
	if !reflect.DeepEqual(providers, []string{"openai"}) {
		t.Fatalf("providers = %v, want [openai]", providers)
	}
	if model != "gpt-5" {
		t.Fatalf("model = %q, want gpt-5", model)
	}
}

func TestGetRequestDetails_RouteGroupRejectsConflictingModelPrefix(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreauth.NewManager(nil, nil, nil))
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Set(internalrouting.GinPathRouteContextKey, &internalrouting.PathRouteContext{
		RoutePath: "/pro",
		Group:     "pro",
		Fallback:  "none",
	})
	ctx := context.WithValue(context.Background(), util.ContextKeyGin, ginCtx)

	_, _, errMsg := handler.getRequestDetails(ctx, "free/gpt-5")
	if errMsg == nil {
		t.Fatal("expected model_prefix_conflict error")
	}
	if errMsg.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", errMsg.StatusCode)
	}
}

func TestRequestExecutionMetadata_UsesPathRouteContextFromRequestContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest("POST", "/openai/plus/v1/responses", nil)
	route := &internalrouting.PathRouteContext{
		RoutePath: "/openai/plus",
		Group:     "pro",
		Fallback:  "none",
	}
	ctx := internalrouting.WithPathRouteContext(context.Background(), route)
	ctx = context.WithValue(ctx, util.ContextKeyGin, ginCtx)

	meta := requestExecutionMetadata(ctx)
	if got := meta["route_group"]; got != "pro" {
		t.Fatalf("route_group = %v, want %q", got, "pro")
	}
	if got := meta["route_fallback"]; got != "none" {
		t.Fatalf("route_fallback = %v, want %q", got, "none")
	}
}

func TestRequestExecutionMetadata_UsesGinRequestContextPathRouteAfterHandleContextReset(t *testing.T) {
	gin.SetMode(gin.TestMode)

	route := &internalrouting.PathRouteContext{
		RoutePath: "/openai/plus",
		Group:     "pro",
		Fallback:  "none",
	}
	req := httptest.NewRequest("POST", "/v1/responses", nil)
	req = req.WithContext(internalrouting.WithPathRouteContext(req.Context(), route))

	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = req

	ctx := context.WithValue(context.Background(), util.ContextKeyGin, ginCtx)
	meta := requestExecutionMetadata(ctx)
	if got := meta["route_group"]; got != "pro" {
		t.Fatalf("route_group = %v, want %q", got, "pro")
	}
	if got := meta["route_fallback"]; got != "none" {
		t.Fatalf("route_fallback = %v, want %q", got, "none")
	}
}

func TestRequestExecutionMetadata_TrustedClaudeProxySecretAddsAffinity(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const secret = "relay-secret"
	t.Setenv("CLAUDE_UPSTREAM_PROXY_SECRET", secret)
	t.Setenv("CLAUDE_PROXY_SECRET", "")

	req := httptest.NewRequest("POST", "/anthropic/v1/messages", nil)
	req.Header.Set(theClawBaySignedUserIDHeader, "user-1")
	req.Header.Set(theClawBaySignedAPIKeyIDHeader, "key-1")
	req.Header.Set(claudeProxySecretHeader, secret)

	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = req

	ctx := context.WithValue(context.Background(), util.ContextKeyGin, ginCtx)
	meta := requestExecutionMetadata(ctx)
	if got := meta[coreexecutor.AuthAffinityMetadataKey]; got != "tcb:user-1:key-1" {
		t.Fatalf("auth_affinity_key = %v, want %q", got, "tcb:user-1:key-1")
	}
}

func TestRequestExecutionMetadata_ValidGatewaySignatureAddsAffinity(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const signingSecret = "signing-secret"
	t.Setenv("CLAUDE_UPSTREAM_PROXY_SECRET", "")
	t.Setenv("CLAUDE_PROXY_SECRET", "")
	t.Setenv("CODEX_LB_PROXY_SIGNING_SECRET", signingSecret)

	req := httptest.NewRequest("POST", "/anthropic/v1/messages?beta=true", nil)
	req.Header.Set(theClawBaySignedUserIDHeader, "user-2")
	req.Header.Set(theClawBaySignedAPIKeyIDHeader, "key-2")
	req.Header.Set(theClawBaySignedPlanMultiplierHeader, "3")
	signedAt := time.Now().UTC().Format(time.RFC3339)
	nonce := "abcdef0123456789"
	req.Header.Set(theClawBaySignedAtHeader, signedAt)
	req.Header.Set(theClawBaySignedNonceHeader, nonce)
	req.Header.Set(
		theClawBaySignatureHeader,
		signGatewayRequestForTest(req, signingSecret, "user-2", "key-2", "3", signedAt, nonce),
	)

	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = req

	ctx := context.WithValue(context.Background(), util.ContextKeyGin, ginCtx)
	meta := requestExecutionMetadata(ctx)
	if got := meta[coreexecutor.AuthAffinityMetadataKey]; got != "tcb:user-2:key-2" {
		t.Fatalf("auth_affinity_key = %v, want %q", got, "tcb:user-2:key-2")
	}
}

func TestRequestExecutionMetadata_InvalidGatewaySignatureDoesNotAddAffinity(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Setenv("CLAUDE_UPSTREAM_PROXY_SECRET", "")
	t.Setenv("CLAUDE_PROXY_SECRET", "")
	t.Setenv("CODEX_LB_PROXY_SIGNING_SECRET", "signing-secret")

	req := httptest.NewRequest("POST", "/anthropic/v1/messages", nil)
	req.Header.Set(theClawBaySignedUserIDHeader, "user-3")
	req.Header.Set(theClawBaySignedAPIKeyIDHeader, "key-3")
	req.Header.Set(theClawBaySignedPlanMultiplierHeader, "1")
	req.Header.Set(theClawBaySignedAtHeader, time.Now().UTC().Format(time.RFC3339))
	req.Header.Set(theClawBaySignedNonceHeader, "nonce")
	req.Header.Set(theClawBaySignatureHeader, "bad-signature")

	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = req

	ctx := context.WithValue(context.Background(), util.ContextKeyGin, ginCtx)
	meta := requestExecutionMetadata(ctx)
	if _, exists := meta[coreexecutor.AuthAffinityMetadataKey]; exists {
		t.Fatalf("unexpected affinity key in metadata: %v", meta)
	}
}

func signGatewayRequestForTest(
	req *http.Request,
	secret string,
	userID string,
	apiKeyID string,
	planMultiplier string,
	signedAt string,
	nonce string,
) string {
	pathWithSearch := req.URL.RequestURI()
	payload := fmt.Sprintf(
		"%s\n%s\n%s\n%s\n%s\n%s\n%s",
		strings.ToUpper(req.Method),
		pathWithSearch,
		userID,
		apiKeyID,
		planMultiplier,
		signedAt,
		nonce,
	)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return fmt.Sprintf("%x", mac.Sum(nil))
}
