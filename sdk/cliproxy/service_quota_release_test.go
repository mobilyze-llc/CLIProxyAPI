package cliproxy

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const (
	releaseOpus  = "claude-opus-5-5"
	releaseHaiku = "claude-haiku-5-5"
)

type releaseTestExecutor struct {
	*executor.ClaudeExecutor
	served chan string
}

func (e releaseTestExecutor) Execute(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.served <- auth.ID
	return coreexecutor.Response{Payload: []byte(`{}`)}, nil
}

func claudeWindow(utilization float64, resetAt time.Time) string {
	return fmt.Sprintf(`{"utilization":%v,"resets_at":%q}`, utilization, resetAt.UTC().Format(time.RFC3339))
}

type releaseFixture struct {
	t       *testing.T
	cfg     *config.Config
	manager *coreauth.Manager
	exec    releaseTestExecutor
	bodies  map[string]func() (int, string)
}

func newReleaseFixture(t *testing.T, cfg *config.Config, store coreauth.CooldownStateStore) *releaseFixture {
	t.Helper()
	coreauth.SetQuotaCooldownDisabled(false)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.SetCooldownStateStore(store)
	exec := releaseTestExecutor{ClaudeExecutor: executor.NewClaudeExecutor(cfg), served: make(chan string, 8)}
	manager.RegisterExecutor(exec)
	f := &releaseFixture{t: t, cfg: cfg, manager: manager, exec: exec, bodies: map[string]func() (int, string){}}
	expiry := time.Now().Add(10 * 24 * time.Hour).Format(time.RFC3339)
	for _, id := range []string{"restored", "still-exhausted"} {
		registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: releaseOpus}, {ID: releaseHaiku}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		auth := &coreauth.Auth{ID: id, Provider: "claude", Status: coreauth.StatusActive,
			Metadata: map[string]any{"type": "claude", "email": id + "@example.com", "access_token": "tok-" + id, "expired": expiry}}
		if _, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), auth); errRegister != nil {
			t.Fatal(errRegister)
		}
	}
	return f
}

func (f *releaseFixture) fail(authID, model string, status int, retryAfter time.Duration, credentialScope bool) {
	f.manager.MarkResult(context.Background(), coreauth.Result{
		AuthID: authID, Provider: "claude", Model: model, CredentialScope: credentialScope, RetryAfter: &retryAfter,
		Error: &coreauth.Error{HTTPStatus: status, Message: fmt.Sprintf("upstream %d", status)},
	})
}

func (f *releaseFixture) seed() {
	f.fail("restored", releaseHaiku, http.StatusNotFound, 12*time.Hour, false)
	f.fail("restored", releaseOpus, http.StatusTooManyRequests, 4*24*time.Hour, true)
	f.fail("still-exhausted", releaseOpus, http.StatusTooManyRequests, 4*24*time.Hour, true)
	for _, id := range []string{"restored", "still-exhausted"} {
		if auth, _ := f.manager.GetByID(id); !auth.Quota.Exceeded || auth.Quota.Reason != "credential_quota" {
			f.t.Fatalf("%s seed quota = %+v, want an active credential_quota", id, auth.Quota)
		}
	}
}

func (f *releaseFixture) readAll() {
	f.t.Helper()
	rt := quotaTestRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "api.anthropic.com" || req.URL.Path != "/api/oauth/usage" {
			return nil, fmt.Errorf("unexpected upstream request %s %s", req.Method, req.URL)
		}
		id := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer tok-")
		status, body := f.bodies[id]()
		return quotaTestResponse(req, status, "application/json", body), nil
	})
	service := &Service{cfg: f.cfg, coreManager: f.manager}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(rt))
	for _, id := range []string{"restored", "still-exhausted"} {
		service.readQuotaUsage(ctx, id)
	}
}

func (f *releaseFixture) execute(model string) (string, error) {
	_, errExec := f.manager.Execute(context.Background(), []string{"claude"}, coreexecutor.Request{Model: model}, coreexecutor.Options{})
	if errExec != nil {
		return "", errExec
	}
	return <-f.exec.served, nil
}

func available(utilization float64) func() (int, string) {
	return func() (int, string) {
		return http.StatusOK, fmt.Sprintf(`{"five_hour":null,"seven_day":%s,"extra_usage":null}`, claudeWindow(utilization, time.Now().Add(7*24*time.Hour)))
	}
}

func assertReleased(t *testing.T, f *releaseFixture) {
	t.Helper()
	if served, errExec := f.execute(releaseOpus); errExec != nil || served != "restored" {
		t.Fatalf("opus after the usage read served by %q, err %v; want the restored account", served, errExec)
	}
	if served, errExec := f.execute(releaseHaiku); errExec == nil {
		t.Fatalf("haiku served by %q; its own 404 cooldown must survive the release", served)
	}
	reg := registry.GetGlobalRegistry()
	if reg.IsModelSuspendedForClient("restored", releaseOpus) {
		t.Error("registry still suspends opus on the restored account")
	}
	if !reg.IsModelSuspendedForClient("restored", releaseHaiku) {
		t.Error("registry resumed haiku on the restored account despite its own cooldown")
	}
	if !reg.IsModelSuspendedForClient("still-exhausted", releaseOpus) {
		t.Error("registry resumed opus on the still-exhausted account")
	}
	if auth, _ := f.manager.GetByID("still-exhausted"); auth.Quota.Reason != "credential_quota" {
		t.Errorf("still-exhausted quota = %+v, want its credential_quota kept", auth.Quota)
	}
}

func TestClaudeUsageReadReleasesStaleCredentialQuota(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reserves []config.QuotaReserve
	}{
		{name: "scheduler"},
		{name: "reserve-selection", reserves: []config.QuotaReserve{{Provider: "codex", Email: "personal@example.com", Percent: 20}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{SaveCooldownStatus: true, Routing: config.RoutingConfig{QuotaReserves: tc.reserves}}
			store := &quotaTestCooldownStore{}
			f := newReleaseFixture(t, cfg, store)
			f.seed()
			f.bodies["restored"] = available(5)
			f.bodies["still-exhausted"] = available(100)
			f.readAll()

			assertReleased(t, f)
			store.mu.Lock()
			defer store.mu.Unlock()
			for _, record := range store.records {
				if record.AuthID == "restored" && (record.Quota.Reason == "credential_quota" || record.Quota.Exceeded && record.Model == "") {
					t.Errorf("saved record %q/%q still holds the released quota %+v", record.AuthID, record.Model, record.Quota)
				}
			}
		})
	}
}

func TestClaudeUsageReleaseSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{SaveCooldownStatus: true}
	f := newReleaseFixture(t, cfg, coreauth.NewFileCooldownStateStore(dir))
	f.seed()
	f.bodies["restored"] = available(5)
	f.bodies["still-exhausted"] = available(100)
	f.readAll()

	restarted := newReleaseFixture(t, cfg, coreauth.NewFileCooldownStateStore(dir))
	if errRestore := restarted.manager.RestoreCooldownStates(context.Background()); errRestore != nil {
		t.Fatal(errRestore)
	}
	if auth, _ := restarted.manager.GetByID("restored"); auth.Quota.Exceeded {
		t.Fatalf("restart restored quota %+v on the released account", auth.Quota)
	}
	ctx := coreauth.WithSkipPersist(context.Background())
	if selected, errSelect := restarted.manager.SelectAuth(ctx, "claude", releaseOpus, coreexecutor.Options{}); errSelect != nil || selected.ID != "restored" {
		t.Fatalf("opus selection after restart = %v, %v; want the restored account", selected, errSelect)
	}
	if selected, errSelect := restarted.manager.SelectAuth(ctx, "claude", releaseHaiku, coreexecutor.Options{}); errSelect == nil {
		t.Fatalf("haiku selection after restart admitted %s despite its own cooldown", selected.ID)
	}
	if auth, _ := restarted.manager.GetByID("still-exhausted"); auth.Quota.Reason != "credential_quota" {
		t.Errorf("still-exhausted quota after restart = %+v, want its credential_quota", auth.Quota)
	}
}

func TestClaudeUsageReadWithoutProofKeepsCredentialQuota(t *testing.T) {
	future := time.Now().Add(7 * 24 * time.Hour)
	ok := func(body string) func() (int, string) { return func() (int, string) { return http.StatusOK, body } }
	for _, tc := range []struct {
		name string
		body func() (int, string)
	}{
		{"hollow", ok(`{"five_hour":{"utilization":null,"resets_at":null},"seven_day":{"utilization":null,"resets_at":null}}`)},
		{"five-hour-missing", ok(fmt.Sprintf(`{"seven_day":%s}`, claudeWindow(5, future)))},
		{"empty-object", ok(fmt.Sprintf(`{"five_hour":{},"seven_day":%s}`, claudeWindow(5, future)))},
		{"seven-day-exhausted", ok(fmt.Sprintf(`{"five_hour":null,"seven_day":%s}`, claudeWindow(100, future)))},
		{"five-hour-exhausted", ok(fmt.Sprintf(`{"five_hour":%s,"seven_day":%s}`, claudeWindow(100, future), claudeWindow(5, future)))},
		{"both-null", ok(`{"five_hour":null,"seven_day":null}`)},
		{"past-reset", ok(fmt.Sprintf(`{"five_hour":null,"seven_day":%s}`, claudeWindow(5, time.Now().Add(-time.Minute))))},
		{"rate-limited", func() (int, string) { return http.StatusTooManyRequests, `{"error":"rate limited"}` }},
		{"server-error", func() (int, string) { return http.StatusInternalServerError, `{}` }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReleaseFixture(t, &config.Config{}, nil)
			f.seed()
			f.bodies["restored"] = tc.body
			f.bodies["still-exhausted"] = available(100)
			f.readAll()
			if auth, _ := f.manager.GetByID("restored"); auth.Quota.Reason != "credential_quota" {
				t.Fatalf("quota after the read = %+v, want the credential_quota kept", auth.Quota)
			}
			if served, errExec := f.execute(releaseOpus); errExec == nil {
				t.Fatalf("opus served by %q without proof of available quota", served)
			}
		})
	}
}

func TestClaudeUsageReadRacingNewQuotaFailureKeepsCooldown(t *testing.T) {
	f := newReleaseFixture(t, &config.Config{}, nil)
	f.seed()
	f.bodies["restored"] = func() (int, string) {
		f.fail("restored", releaseOpus, http.StatusTooManyRequests, 4*24*time.Hour, true)
		return available(5)()
	}
	f.bodies["still-exhausted"] = available(100)
	f.readAll()
	if auth, _ := f.manager.GetByID("restored"); auth.Quota.Reason != "credential_quota" {
		t.Fatalf("quota after a racing failure = %+v, want the credential_quota kept", auth.Quota)
	}
}
