package claude

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestGenerateAuthURLWithRedirectURIUsesProvidedRedirect(t *testing.T) {
	auth := &ClaudeAuth{}
	pkceCodes := &PKCECodes{
		CodeVerifier:  "verifier",
		CodeChallenge: "challenge",
	}

	authURL, state, err := auth.GenerateAuthURLWithRedirectURI("state-123", pkceCodes, PlatformRedirectURI)
	if err != nil {
		t.Fatalf("GenerateAuthURLWithRedirectURI returned error: %v", err)
	}
	if state != "state-123" {
		t.Fatalf("state = %q, want %q", state, "state-123")
	}

	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}
	if got := parsed.Scheme + "://" + parsed.Host + parsed.Path; got != AuthURL {
		t.Fatalf("auth url = %q, want %q", got, AuthURL)
	}
	if got := parsed.Query().Get("redirect_uri"); got != PlatformRedirectURI {
		t.Fatalf("redirect_uri = %q, want %q", got, PlatformRedirectURI)
	}
}

func TestExchangeCodeForTokensWithRedirectURIUsesProvidedRedirect(t *testing.T) {
	var requestBody map[string]any
	auth := &ClaudeAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != TokenURL {
					t.Fatalf("request URL = %q, want %q", req.URL.String(), TokenURL)
				}
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatalf("read request body: %v", err)
				}
				if err := json.Unmarshal(body, &requestBody); err != nil {
					t.Fatalf("unmarshal request body: %v", err)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: io.NopCloser(strings.NewReader(`{
						"access_token":"access-token",
						"refresh_token":"refresh-token",
						"token_type":"Bearer",
						"expires_in":3600,
						"account":{"uuid":"account-uuid-123","email_address":"user@example.com"}
					}`)),
					Request: req,
				}, nil
			}),
		},
	}

	bundle, err := auth.ExchangeCodeForTokensWithRedirectURI(context.Background(), "code-123", "state-123", &PKCECodes{
		CodeVerifier:  "verifier",
		CodeChallenge: "challenge",
	}, PlatformRedirectURI)
	if err != nil {
		t.Fatalf("ExchangeCodeForTokensWithRedirectURI returned error: %v", err)
	}
	if bundle.TokenData.Email != "user@example.com" {
		t.Fatalf("email = %q, want %q", bundle.TokenData.Email, "user@example.com")
	}
	if bundle.TokenData.AccountUUID != "account-uuid-123" {
		t.Fatalf("account uuid = %q, want %q", bundle.TokenData.AccountUUID, "account-uuid-123")
	}
	if got := requestBody["redirect_uri"]; got != PlatformRedirectURI {
		t.Fatalf("redirect_uri = %v, want %q", got, PlatformRedirectURI)
	}
}

func TestExchangeCodeForTokensWithRedirectURIDecodesGzipBodyWithoutHeader(t *testing.T) {
	var requestBody map[string]any
	auth := &ClaudeAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatalf("read request body: %v", err)
				}
				if err := json.Unmarshal(body, &requestBody); err != nil {
					t.Fatalf("unmarshal request body: %v", err)
				}
				payload := gzipJSON(t, map[string]any{
					"access_token":  "access-token",
					"refresh_token": "refresh-token",
					"token_type":    "Bearer",
					"expires_in":    3600,
					"account": map[string]any{
						"uuid":          "account-uuid-123",
						"email_address": "user@example.com",
					},
				})
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(bytes.NewReader(payload)),
					Request:    req,
				}, nil
			}),
		},
	}

	bundle, err := auth.ExchangeCodeForTokensWithRedirectURI(context.Background(), "code-123", "state-123", &PKCECodes{
		CodeVerifier:  "verifier",
		CodeChallenge: "challenge",
	}, PlatformRedirectURI)
	if err != nil {
		t.Fatalf("ExchangeCodeForTokensWithRedirectURI returned error: %v", err)
	}
	if bundle.TokenData.Email != "user@example.com" {
		t.Fatalf("email = %q, want %q", bundle.TokenData.Email, "user@example.com")
	}
	if got := requestBody["redirect_uri"]; got != PlatformRedirectURI {
		t.Fatalf("redirect_uri = %v, want %q", got, PlatformRedirectURI)
	}
}

func TestRefreshTokensDecodesGzipErrorBodyWithoutHeader(t *testing.T) {
	auth := &ClaudeAuth{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				payload := gzipJSON(t, map[string]any{
					"error":             "invalid_grant",
					"error_description": "Refresh token not found or invalid",
				})
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Header:     make(http.Header),
					Body:       io.NopCloser(bytes.NewReader(payload)),
					Request:    req,
				}, nil
			}),
		},
	}

	_, err := auth.RefreshTokens(context.Background(), "refresh-token")
	if err == nil {
		t.Fatal("expected refresh error")
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("error = %q, want invalid_grant", err.Error())
	}
	if strings.Contains(err.Error(), "\x1f\x8b") {
		t.Fatalf("error still contains gzip bytes: %q", err.Error())
	}
}

func gzipJSON(t *testing.T, payload any) []byte {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal gzip payload: %v", err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		t.Fatalf("write gzip payload: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close gzip payload: %v", err)
	}
	return buf.Bytes()
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
