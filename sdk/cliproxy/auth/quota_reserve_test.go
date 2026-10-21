package auth

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func reserveTestAuth(provider, id, email string, fiveHour, weekly float64, reset time.Time) *Auth {
	var auth *Auth
	if provider == "claude" {
		auth = claudeAuthWithWindows(id, fiveHour/100, reset, reset)
		auth.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Utilization"] = strconv.FormatFloat(weekly/100, 'f', -1, 64)
	} else {
		auth = codexAuthWithSignals(id, codexWindowSignals("Primary", weeklyWindowMinutes, weekly, reset), codexWindowSignals("Secondary", 300, fiveHour, reset))
	}
	auth.Metadata = map[string]any{"email": email, "access_token": "synthetic-token"}
	auth.Status = StatusActive
	return auth
}

func reserveTestConfig(provider string) *config.Config {
	return &config.Config{Routing: config.RoutingConfig{QuotaReserves: []config.QuotaReserve{{Provider: provider, Email: "personal@example.com", Percent: 20}}}}
}

func TestQuotaReserveThresholds(t *testing.T) {
	now := time.Now()
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(reserveTestConfig(provider))
			cases := []struct {
				name         string
				five, weekly float64
				email        string
				reset        time.Time
				blocked      bool
			}{
				{"below", 79.99, 79.99, "personal@example.com", now.Add(time.Hour), false},
				{"five-hour", 80, 10, "personal@example.com", now.Add(time.Hour), true},
				{"weekly", 10, 80, "personal@example.com", now.Add(time.Hour), true},
				{"over", 99, 99, "personal@example.com", now.Add(time.Hour), true},
				{"other-account", 99, 99, "other@example.com", now.Add(time.Hour), false},
				{"case-insensitive", 80, 10, " Personal@Example.com ", now.Add(time.Hour), true},
				{"reset", 99, 99, "personal@example.com", now.Add(-time.Second), false},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					auth := reserveTestAuth(provider, "threshold", tc.email, tc.five, tc.weekly, tc.reset)
					if got := manager.quotaReserveReached(auth, now); got != tc.blocked {
						t.Fatalf("blocked=%v, want %v", got, tc.blocked)
					}
				})
			}
			auth := reserveTestAuth(provider, "unknown", "personal@example.com", 0, 0, now.Add(time.Hour))
			auth.Quota = QuotaState{}
			if !manager.quotaReserveReached(auth, now) {
				t.Fatal("unobserved protected account must wait for usage read")
			}
			manager.SetConfig(&config.Config{})
			if manager.quotaReserveReached(auth, now) {
				t.Fatal("removing reserve must restore eligibility")
			}
		})
	}
}

func TestQuotaReserveExistingSessionsAndSelectionPaths(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, strategy := range []string{"soonest-reset/five-hour", "soonest-reset/weekly", "round-robin/five-hour", "round-robin/weekly"} {
			for _, mixed := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/mixed=%v", provider, strategy, mixed), func(t *testing.T) {
					now := time.Now()
					id := fmt.Sprintf("reserve-%s-%s-%v", provider, strategy, mixed)
					protected := reserveTestAuth(provider, id, "personal@example.com", 10, 10, now.Add(time.Hour))
					manager := NewManager(nil, nil, nil)
					var fallback Selector = &SoonestResetSelector{}
					if strings.HasPrefix(strategy, "round-robin") {
						fallback = &RoundRobinSelector{}
					}
					affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: fallback, TTL: time.Hour})
					t.Cleanup(affinity.Stop)
					manager.SetSelector(affinity)
					manager.SetConfig(reserveTestConfig(provider))
					manager.RegisterExecutor(schedulerTestExecutor{provider: provider})
					register := func(auth *Auth) {
						if _, err := manager.Register(WithSkipPersist(context.Background()), auth); err != nil {
							t.Fatal(err)
						}
						registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: id}})
						t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
					}
					register(protected)
					pick := func() (*Auth, error) {
						opts := cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": []string{id}}}
						if mixed {
							a, _, _, err := manager.pickNextMixed(context.Background(), []string{provider}, id, opts, nil)
							return a, err
						}
						a, _, err := manager.pickNext(context.Background(), provider, id, opts, nil)
						return a, err
					}
					if a, err := pick(); err != nil || a.ID != id {
						t.Fatalf("initial pick=%v err=%v", a, err)
					}
					fiveHour, weekly := 80.0, 10.0
					if strings.HasSuffix(strategy, "weekly") {
						fiveHour, weekly = 10, 80
					}
					exhausted := reserveTestAuth(provider, id, "personal@example.com", fiveHour, weekly, now.Add(time.Hour))
					headers := http.Header{}
					for key, value := range exhausted.Quota.Signals {
						headers.Set(key, value)
					}
					ctx := internallogging.WithResponseHeadersHolder(context.Background())
					internallogging.SetResponseHeaders(ctx, headers)
					manager.MarkResult(ctx, Result{AuthID: id, Provider: provider, Model: id, Success: true})
					if a, err := pick(); err == nil || a != nil {
						t.Fatalf("sticky session bypassed reserve: auth=%v err=%v", a, err)
					}
					other := reserveTestAuth(provider, id+"-other", "other@example.com", 10, 10, now.Add(2*time.Hour))
					register(other)
					if a, err := pick(); err != nil || a.ID != other.ID {
						t.Fatalf("failover pick=%v err=%v", a, err)
					}
					if _, err := manager.SelectAuth(context.Background(), provider, id, cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: id}}); err == nil {
						t.Fatal("pinned protected auth must not bypass reserve")
					}
				})
			}
		}
	}
}

func TestQuotaReserveDisablesSchedulerBypass(t *testing.T) {
	for _, selector := range []Selector{&RoundRobinSelector{}, &WeightedRoundRobinSelector{}, &FillFirstSelector{}} {
		manager := NewManager(nil, selector, nil)
		if !manager.useSchedulerFastPath() {
			t.Fatal("expected scheduler fast path without reserve")
		}
		manager.SetConfig(reserveTestConfig("codex"))
		if manager.useSchedulerFastPath() {
			t.Fatal("reserve must use request-time candidate filtering")
		}
	}
}

func TestQuotaReservePluginDelegation(t *testing.T) {
	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.SetConfig(reserveTestConfig("codex"))
	manager.RegisterExecutor(schedulerTestExecutor{provider: "codex"})
	manager.SetPluginScheduler(&fakePluginScheduler{resp: pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: pluginapi.SchedulerBuiltinFillFirst}, handled: true})
	for _, auth := range []*Auth{
		reserveTestAuth("codex", "reserve-delegate-a", "personal@example.com", 80, 10, time.Now().Add(time.Hour)),
		reserveTestAuth("codex", "reserve-delegate-b", "other@example.com", 10, 10, time.Now().Add(time.Hour)),
	} {
		if _, err := manager.Register(WithSkipPersist(context.Background()), auth); err != nil {
			t.Fatal(err)
		}
	}
	for _, mixed := range []bool{false, true} {
		var got *Auth
		var err error
		if mixed {
			got, _, _, err = manager.pickNextMixed(context.Background(), []string{"codex"}, "", cliproxyexecutor.Options{}, nil)
		} else {
			got, _, err = manager.pickNext(context.Background(), "codex", "", cliproxyexecutor.Options{}, nil)
		}
		if err != nil || got.ID != "reserve-delegate-b" {
			t.Fatalf("delegated pick=%v err=%v", got, err)
		}
	}
}

func TestQuotaReserveConcurrentUsageAndReload(t *testing.T) {
	protected := reserveTestAuth("codex", "reserve-race-a", "personal@example.com", 10, 10, time.Now().Add(time.Hour))
	other := reserveTestAuth("codex", "reserve-race-b", "other@example.com", 10, 10, time.Now().Add(2*time.Hour))
	manager := newSoonestResetManager(t, nil, protected, other)
	manager.SetConfig(reserveTestConfig("codex"))
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			manager.ObserveQuotaHeaders(protected.ID, codexWeeklyHeaders(time.Now().Add(time.Hour)))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			manager.SetConfig(reserveTestConfig("codex"))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			pickSession(t, manager, strconv.Itoa(i))
		}
	}()
	wg.Wait()
}
