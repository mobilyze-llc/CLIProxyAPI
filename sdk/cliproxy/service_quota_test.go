package cliproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type quotaTestStore struct {
	auths []*coreauth.Auth
	saves atomic.Int32
}

func (s *quotaTestStore) List(context.Context) ([]*coreauth.Auth, error) {
	out := make([]*coreauth.Auth, 0, len(s.auths))
	for _, auth := range s.auths {
		out = append(out, auth.Clone())
	}
	return out, nil
}

func (s *quotaTestStore) Save(_ context.Context, auth *coreauth.Auth) (string, error) {
	s.saves.Add(1)
	return auth.ID, nil
}

func (s *quotaTestStore) Delete(context.Context, string) error { return nil }

type quotaTestCooldownStore struct {
	mu      sync.Mutex
	saves   int
	records []coreauth.CooldownStateRecord
}

func (s *quotaTestCooldownStore) Load(context.Context) ([]coreauth.CooldownStateRecord, error) {
	return nil, nil
}

func (s *quotaTestCooldownStore) Save(_ context.Context, records []coreauth.CooldownStateRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	s.records = records
	return nil
}

type quotaTestRoundTripper func(*http.Request) (*http.Response, error)

func (f quotaTestRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type quotaTestRTProvider struct{ rt http.RoundTripper }

func (p quotaTestRTProvider) RoundTripperFor(*coreauth.Auth) http.RoundTripper { return p.rt }

func quotaTestResponse(req *http.Request, status int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func codexWeeklyUsage(resetAt time.Time) string {
	return fmt.Sprintf(`{"plan_type":"pro","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":9,"limit_window_seconds":604800,"reset_after_seconds":%d,"reset_at":%d},"secondary_window":null},"additional_rate_limits":[]}`,
		int64(time.Until(resetAt).Seconds()), resetAt.Unix())
}

func codexWeeklySignals(resetAt time.Time) coreauth.QuotaState {
	return coreauth.QuotaState{ObservedAt: time.Now(), Signals: map[string]string{
		"X-Codex-Primary-Used-Percent":   "50",
		"X-Codex-Primary-Window-Minutes": "10080",
		"X-Codex-Primary-Reset-At":       strconv.FormatInt(resetAt.Unix(), 10),
	}}
}

// TestQuotaUsageSweepRoutesNewSessionToUnusedCredential runs the service with three Codex
// OAuth credentials and one Claude OAuth credential. codex-a has the soonest observed weekly
// reset and its usage read is rejected; codex-c was never used, so without a usage read it
// sorts last. The sweep shows codex-c resetting soonest, so a new session goes to codex-c.
func TestQuotaUsageSweepRoutesNewSessionToUnusedCredential(t *testing.T) {
	// A Codex catalog model, so the request routes whether or not the service has replaced
	// the registrations below with its own catalog registration yet.
	const model = "gpt-5.5"
	now := time.Now()
	resetA, resetB, resetBRead, resetC := now.Add(48*time.Hour), now.Add(72*time.Hour), now.Add(60*time.Hour), now.Add(24*time.Hour)
	// Tokens expire well past every refresh lead and carry no refresh token, so the
	// auto-refresh loop has nothing due and no test path can reach a real token endpoint.
	expiry := now.Add(10 * 24 * time.Hour).Format(time.RFC3339)
	codexAuth := func(id, token string, quota coreauth.QuotaState) *coreauth.Auth {
		return &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive, Quota: quota,
			Metadata: map[string]any{"type": "codex", "access_token": token, "account_id": "account-" + token,
				"expired": expiry}}
	}
	store := &quotaTestStore{auths: []*coreauth.Auth{
		codexAuth("codex-a", "tok-a", codexWeeklySignals(resetA)),
		codexAuth("codex-b", "tok-b", codexWeeklySignals(resetB)),
		codexAuth("codex-c", "tok-c", coreauth.QuotaState{}),
		{ID: "claude-d", Provider: "claude", Status: coreauth.StatusActive,
			Metadata: map[string]any{"type": "claude", "access_token": "tok-d", "expired": expiry}},
	}}
	for _, auth := range store.auths {
		id, provider := auth.ID, auth.Provider
		registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}

	var mu sync.Mutex
	var usageReads, responses, unexpected []string
	rt := quotaTestRoundTripper(func(req *http.Request) (*http.Response, error) {
		token := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
		route := req.Method + " " + req.URL.Host + req.URL.Path
		mu.Lock()
		defer mu.Unlock()
		switch route {
		case "GET chatgpt.com/backend-api/wham/usage":
			usageReads = append(usageReads, token)
			if req.Header.Get("Chatgpt-Account-Id") != "account-"+token {
				unexpected = append(unexpected, "missing account header for "+token)
			}
			switch token {
			case "tok-a":
				return quotaTestResponse(req, http.StatusTooManyRequests, "application/json", `{"error":"rate limited"}`), nil
			case "tok-b":
				return quotaTestResponse(req, http.StatusOK, "application/json", codexWeeklyUsage(resetBRead)), nil
			case "tok-c":
				return quotaTestResponse(req, http.StatusOK, "application/json", codexWeeklyUsage(resetC)), nil
			}
		case "GET api.anthropic.com/api/oauth/usage":
			usageReads = append(usageReads, token)
			if req.Header.Get("Anthropic-Beta") != "oauth-2025-04-20" {
				unexpected = append(unexpected, "missing anthropic-beta header")
			}
			return quotaTestResponse(req, http.StatusOK, "application/json",
				`{"five_hour":{"utilization":14.0,"resets_at":"2026-10-04T02:40:00.347417+00:00"},"seven_day":{"utilization":31.0,"resets_at":"2026-10-08T23:00:00.347435+00:00"}}`), nil
		case "POST chatgpt.com/backend-api/codex/responses":
			responses = append(responses, token)
			completed := fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp-1","object":"response","status":"completed","model":%q,"output":[{"type":"message","id":"msg-1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`, model)
			return quotaTestResponse(req, http.StatusOK, "text/event-stream", "data: "+completed+"\n\n"), nil
		}
		unexpected = append(unexpected, route)
		return nil, errors.New("unexpected upstream request: " + route)
	})

	ln, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if errClose := ln.Close(); errClose != nil {
		t.Fatalf("close listener: %v", errClose)
	}
	authDir := t.TempDir()
	cfgPath := filepath.Join(authDir, "config.yaml")
	if errWrite := os.WriteFile(cfgPath, []byte("{}"), 0o644); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}
	cfg := &config.Config{Host: "127.0.0.1", Port: port, AuthDir: authDir, SaveCooldownStatus: true}
	cfg.APIKeys = []string{"proxy-key"}
	cfg.Routing.Strategy = "soonest-reset"
	cooldownStore := &quotaTestCooldownStore{}
	manager := coreauth.NewManager(store, newRoutingSelector(normalizedRoutingRuntimeState(cfg)), nil)
	service, errBuild := NewBuilder().
		WithConfig(cfg).
		WithConfigPath(cfgPath).
		WithCoreAuthManager(manager).
		WithCooldownStateStore(cooldownStore).
		WithWatcherFactory(func(string, string, func(*config.Config)) (*WatcherWrapper, error) {
			return &WatcherWrapper{}, nil
		}).
		Build()
	if errBuild != nil {
		t.Fatalf("build service: %v", errBuild)
	}
	// Execution reads the transport from the round-tripper provider; the sweep, which runs
	// outside any request, reads it from the service context.
	manager.SetRoundTripperProvider(quotaTestRTProvider{rt: rt})
	runCtx, cancel := context.WithCancel(context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(rt)))
	runDone := make(chan error, 1)
	go func() { runDone <- service.Run(runCtx) }()
	defer func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(10 * time.Second):
			t.Error("timed out waiting for service.Run to exit")
		}
	}()

	resetOf := func(id string) string {
		auth, _ := manager.GetByID(id)
		if auth == nil {
			return ""
		}
		return auth.Quota.Signals["X-Codex-Primary-Reset-At"]
	}
	swept := func() bool {
		mu.Lock()
		reads := len(usageReads)
		mu.Unlock()
		claude, _ := manager.GetByID("claude-d")
		return reads == 4 && claude != nil && claude.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Reset"] != "" &&
			resetOf("codex-b") == strconv.FormatInt(resetBRead.Unix(), 10) &&
			resetOf("codex-c") == strconv.FormatInt(resetC.Unix(), 10)
	}
	for deadline := time.Now().Add(5 * time.Second); !swept() && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	t.Logf("usage reads: %v", usageReads)
	if len(unexpected) != 0 {
		t.Errorf("unexpected upstream traffic during the sweep (refreshes included): %v", unexpected)
	}
	mu.Unlock()
	if saves := store.saves.Load(); saves != 0 {
		t.Errorf("token store saves during the sweep = %d, want 0", saves)
	}
	cooldownStore.mu.Lock()
	cdsSaves := cooldownStore.saves
	cooldownStore.mu.Unlock()
	if cdsSaves == 0 {
		t.Error("cooldown state store was not written after the sweep")
	}
	if got, want := resetOf("codex-a"), strconv.FormatInt(resetA.Unix(), 10); got != want {
		t.Errorf("codex-a reset after a rejected read = %q, want previous %q", got, want)
	}
	for _, id := range []string{"codex-a", "codex-b", "codex-c", "claude-d"} {
		auth, _ := manager.GetByID(id)
		if token, _ := auth.Metadata["access_token"].(string); token != "tok-"+id[len(id)-1:] {
			t.Errorf("%s access token changed during the sweep: %q", id, token)
		}
		// A refresh attempt sets NextRefreshAfter when it starts and LastRefreshedAt on success.
		if !auth.NextRefreshAfter.IsZero() || !auth.LastRefreshedAt.IsZero() {
			t.Errorf("%s was refreshed during the sweep", id)
		}
	}

	var resp *http.Response
	var errDo error
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		req, errRequest := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/v1/responses", port),
			strings.NewReader(fmt.Sprintf(`{"model":%q,"input":"hello"}`, model)))
		if errRequest != nil {
			t.Fatalf("new request: %v", errRequest)
		}
		req.Header.Set("Authorization", "Bearer proxy-key")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Session-Id", fmt.Sprintf("session-%d", time.Now().UnixNano()))
		if resp, errDo = http.DefaultClient.Do(req); errDo == nil {
			break
		}
	}
	if errDo != nil {
		t.Fatalf("proxy request: %v", errDo)
	}
	body, _ := io.ReadAll(resp.Body)
	if errClose := resp.Body.Close(); errClose != nil {
		t.Errorf("close response body: %v", errClose)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("proxy status = %d, body = %s", resp.StatusCode, body)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(responses) != 1 || responses[0] != "tok-c" {
		t.Fatalf("new session went to %v, want [tok-c]: the never-used credential resets soonest", responses)
	}
}
