package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type usageReadCapture struct {
	method string
	url    string
	header http.Header
}

// captureUsageRead sends one usage read through the executor and returns what the
// transport received. The injected round tripper answers every request, so the
// read cannot reach a real endpoint.
func captureUsageRead(t *testing.T, httpRequest func(context.Context, *cliproxyauth.Auth, *http.Request) (*http.Response, error), auth *cliproxyauth.Auth, usageURL string) usageReadCapture {
	t.Helper()
	var captured []usageReadCapture
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		captured = append(captured, usageReadCapture{method: req.Method, url: req.URL.String(), header: req.Header.Clone()})
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: req}, nil
	}))
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, usageURL, nil)
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	resp, errDo := httpRequest(ctx, auth, req)
	if errDo != nil {
		t.Fatalf("usage read: %v", errDo)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	if len(captured) != 1 {
		t.Fatalf("transport received %d requests, want 1", len(captured))
	}
	return captured[0]
}

func usageReadOAuthAuth(provider string, attrs map[string]string) *cliproxyauth.Auth {
	// No refresh token and a far expiry, so nothing can attempt a refresh.
	return &cliproxyauth.Auth{ID: provider + "-usage", Provider: provider, Attributes: attrs, Metadata: map[string]any{
		"type": provider, "access_token": "tok-" + provider, "account_id": "account-1",
		"expired": time.Now().Add(240 * time.Hour).Format(time.RFC3339),
	}}
}

// TestUsageReadTransportReceivesCLIIdentity compares the full request each provider's
// usage read hands its transport against the native CLI header set, written out here
// rather than derived from the helpers under test.
func TestUsageReadTransportReceivesCLIIdentity(t *testing.T) {
	cfg := &config.Config{}
	cfg.ClaudeHeaderDefaults.UserAgent = "claude-cli/2.1.288 (external, cli)"

	claude := captureUsageRead(t, NewClaudeExecutor(cfg).HttpRequest, usageReadOAuthAuth("claude", nil), "https://api.anthropic.com/api/oauth/usage")
	wantClaude := http.Header{
		"Accept":          {"application/json, text/plain, */*"},
		"Content-Type":    {"application/json"},
		"User-Agent":      {"claude-cli/2.1.288 (external, cli)"},
		"Authorization":   {"Bearer tok-claude"},
		"Anthropic-Beta":  {"oauth-2025-04-20"},
		"Accept-Encoding": {"gzip, compress, deflate, br"},
		"Connection":      {"close"},
	}
	if claude.method != http.MethodGet || claude.url != "https://api.anthropic.com/api/oauth/usage" {
		t.Errorf("Claude usage read = %s %s", claude.method, claude.url)
	}
	if !reflect.DeepEqual(claude.header, wantClaude) {
		t.Errorf("Claude usage read headers =\n%v\nwant\n%v", claude.header, wantClaude)
	}

	codex := captureUsageRead(t, NewCodexExecutor(cfg).HttpRequest, usageReadOAuthAuth("codex", nil), "https://chatgpt.com/backend-api/wham/usage")
	wantCodex := http.Header{
		"Authorization":      {"Bearer tok-codex"},
		"User-Agent":         {"codex-tui/0.154.0 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.154.0)"},
		"Originator":         {"codex-tui"},
		"Chatgpt-Account-Id": {"account-1"},
		"Accept":             {"application/json"},
	}
	if codex.method != http.MethodGet || codex.url != "https://chatgpt.com/backend-api/wham/usage" {
		t.Errorf("Codex usage read = %s %s", codex.method, codex.url)
	}
	if !reflect.DeepEqual(codex.header, wantCodex) {
		t.Errorf("Codex usage read headers =\n%v\nwant\n%v", codex.header, wantCodex)
	}
}

// TestCodexUsageReadMatchesInferenceIdentity checks that the usage read sends the
// User-Agent, Originator and account the inference path sends for the same
// credential and configuration.
func TestCodexUsageReadMatchesInferenceIdentity(t *testing.T) {
	const customUA = "custom-agent/1.0"
	cloakingOff := &config.Config{}
	cloakingOff.Codex.DisableCodexCloaking = true
	configuredUA := &config.Config{}
	configuredUA.Codex.DisableCodexCloaking = true
	configuredUA.CodexHeaderDefaults.UserAgent = "codex-configured/2.0"

	cases := []struct {
		name   string
		cfg    *config.Config
		attrs  map[string]string
		wantUA string
	}{
		{name: "cloaking enabled", cfg: &config.Config{}, wantUA: codexUserAgent},
		{name: "cloaking enabled overrides credential User-Agent", cfg: &config.Config{}, attrs: map[string]string{"header:User-Agent": customUA}, wantUA: codexUserAgent},
		{name: "cloaking disabled with credential User-Agent", cfg: cloakingOff, attrs: map[string]string{"header:User-Agent": customUA}, wantUA: customUA},
		{name: "cloaking disabled with configured User-Agent", cfg: configuredUA, wantUA: "codex-configured/2.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth := usageReadOAuthAuth("codex", tc.attrs)
			usage := captureUsageRead(t, NewCodexExecutor(tc.cfg).HttpRequest, auth, "https://chatgpt.com/backend-api/wham/usage")

			inference, errRequest := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
			if errRequest != nil {
				t.Fatal(errRequest)
			}
			apiKey, _ := codexCreds(auth)
			applyCodexHeaders(inference, auth, apiKey, true, tc.cfg, nil)

			for _, name := range []string{"Authorization", "User-Agent", "Originator", "Chatgpt-Account-Id"} {
				if got, want := usage.header.Get(name), inference.Header.Get(name); got != want {
					t.Errorf("usage read %s = %q, inference sends %q", name, got, want)
				}
			}
			if got := usage.header.Get("User-Agent"); got != tc.wantUA {
				t.Errorf("usage read User-Agent = %q, want %q", got, tc.wantUA)
			}
			for _, name := range []string{"Session-Id", "Version", "X-Codex-Beta-Features", "Content-Type", "Connection"} {
				if got := usage.header.Get(name); got != "" {
					t.Errorf("usage read carries inference-only header %s = %q", name, got)
				}
			}
		})
	}
}

// TestClaudeUsageReadDecodesCompressedResponse checks that a gzip-compressed usage
// response, which the advertised Accept-Encoding invites, still parses.
func TestClaudeUsageReadDecodesCompressedResponse(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, errWrite := writer.Write([]byte(`{"five_hour":{"utilization":14.0,"resets_at":"2026-10-04T02:40:00+00:00"},"seven_day":{"utilization":31.0,"resets_at":"2026-10-08T23:00:00+00:00"}}`)); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Request: req,
			Header: http.Header{"Content-Type": {"application/json"}, "Content-Encoding": {"gzip"}},
			Body:   io.NopCloser(bytes.NewReader(compressed.Bytes()))}, nil
	}))
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.anthropic.com/api/oauth/usage", nil)
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	resp, errDo := NewClaudeExecutor(&config.Config{}).HttpRequest(ctx, usageReadOAuthAuth("claude", nil), req)
	if errDo != nil {
		t.Fatalf("usage read: %v", errDo)
	}
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if errClose := resp.Body.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	headers := helps.ParseClaudeUsageHeaders(body)
	if got := headers.Get("Anthropic-Ratelimit-Unified-7d-Utilization"); got != "0.31" {
		t.Fatalf("parsed 7d utilization = %q, want 0.31 (headers %v)", got, headers)
	}
	if got := resp.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("decoded response still declares Content-Encoding %q", got)
	}
}
