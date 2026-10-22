package auth

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestQuotaReserveOldPartialReadingDoesNotOutliveItsReset(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	reset := now.Add(time.Hour)
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			manager.SetConfig(reserveTestConfig(provider))
			auth := reserveTestAuth(provider, "protected", "personal@example.com", 95, 10, reset)
			initial := reserveHeaders(auth.Quota.Signals)
			auth.Quota = QuotaState{}
			auth.Quota.ObserveResponseHeadersForProvider(provider, initial, now)
			partial := initial.Clone()
			weekly := http.Header{"Anthropic-Ratelimit-Unified-7d-Utilization": {"0.1"}, "Anthropic-Ratelimit-Unified-7d-Reset": {unixString(reset.Add(time.Hour))}}
			if provider == "claude" {
				partial.Del("Anthropic-Ratelimit-Unified-5h-Reset")
			} else {
				partial.Del("X-Codex-Secondary-Reset-At")
				weekly = reserveHeaders(codexWindowSignals("Primary", weeklyWindowMinutes, 10, reset.Add(time.Hour)))
			}
			auth.Quota.ObserveResponseHeadersForProvider(provider, partial, reset.Add(-time.Minute))
			if !manager.quotaReserveReached(auth, reset.Add(-time.Minute)) {
				t.Fatal("high partial reading lost the active block")
			}
			auth.Quota.ObserveResponseHeadersForProvider(provider, weekly, reset.Add(time.Second))
			if manager.quotaReserveReached(auth, reset.Add(time.Second)) {
				t.Fatal("replaying an old partial reading resurrected an expired block")
			}
		})
	}
}

func TestQuotaReserveMalformedHeadersRemainPersistable(t *testing.T) {
	ctx := context.Background()
	for _, provider := range []string{"claude", "codex"} {
		for _, sample := range []struct{ name, usage, reset string }{
			{"NaN", "NaN", unixString(time.Now().Add(time.Hour))},
			{"positive-infinity", "+Inf", unixString(time.Now().Add(time.Hour))},
			{"negative-infinity", "-Inf", unixString(time.Now().Add(time.Hour))},
			{"millisecond-reset", "95", strconv.FormatInt(time.Now().UnixMilli(), 10)},
		} {
			t.Run(provider+"/"+sample.name, func(t *testing.T) {
				store := NewFileCooldownStateStore(t.TempDir())
				manager := NewManager(nil, nil, nil)
				manager.SetCooldownStateStore(store)
				if _, err := manager.Register(ctx, &Auth{ID: "account", Provider: provider, Status: StatusActive}); err != nil {
					t.Fatal(err)
				}
				usage := sample.usage
				if provider == "claude" && sample.name == "millisecond-reset" {
					usage = "0.95"
				}
				headers := http.Header{"Anthropic-Ratelimit-Unified-5h-Utilization": {usage}, "Anthropic-Ratelimit-Unified-5h-Reset": {sample.reset}}
				if provider == "codex" {
					headers = http.Header{"X-Codex-Primary-Used-Percent": {usage}, "X-Codex-Primary-Reset-At": {sample.reset}}
				}
				manager.ObserveQuotaHeaders("account", headers)
				records, err := store.Load(ctx)
				if err != nil || len(records) != 1 {
					t.Fatalf("malformed input prevented durable observation: records=%d error=%v", len(records), err)
				}
				for name, values := range headers {
					if records[0].Quota.Signals[name] != values[0] {
						t.Fatalf("raw observation %s was lost", name)
					}
				}
			})
		}
	}
}

func TestQuotaReserveCodexGlobalRejectionDoesNotInventWindows(t *testing.T) {
	now := time.Now()
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(reserveTestConfig("codex"))
	auth := reserveTestAuth("codex", "protected", "personal@example.com", 0, 10, now.Add(time.Hour))
	auth.Quota = QuotaState{}
	weekly := reserveHeaders(codexWindowSignals("Primary", weeklyWindowMinutes, 10, now.Add(time.Hour)))
	auth.Quota.ObserveResponseHeadersForProvider("codex", weekly, now)
	auth.Quota.ObserveResponseHeadersForProvider("codex", http.Header{"X-Codex-Limit-Reached": {"true"}}, now.Add(time.Minute))
	if !manager.quotaReserveReached(auth, now.Add(time.Minute)) {
		t.Fatal("global rejection did not exclude the account")
	}
	auth.Quota.ObserveResponseHeadersForProvider("codex", http.Header{"X-Codex-Plan-Type": {"pro"}}, now.Add(90*time.Second))
	if !manager.quotaReserveReached(auth, now.Add(90*time.Second)) {
		t.Fatal("metadata-only snapshot cleared a credential-wide rejection")
	}
	auth.Quota.ObserveResponseHeadersForProvider("codex", weekly, now.Add(2*time.Minute))
	if manager.quotaReserveReached(auth, now.Add(2*time.Minute)) {
		t.Fatal("a phantom window prevented weekly-only recovery")
	}
}

func TestQuotaReservePartialOnlyPollSurvivesRestartAndReset(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	reset := now.Add(time.Hour)
	store := NewFileCooldownStateStore(t.TempDir())
	first := newObservationPersistManager(t, store, "partial-only")
	headers := http.Header{
		"Anthropic-Ratelimit-Unified-5h-Reset": {unixString(reset)},
		"Anthropic-Ratelimit-Unified-7d-Reset": {unixString(reset)},
	}
	if !first.ObserveQuotaHeaders("partial-only", headers) {
		t.Fatal("partial usage was not recorded")
	}
	observed, _ := first.GetByID("partial-only")
	if len(observed.Quota.Signals) != 0 || !observed.Quota.ObservedAt.IsZero() {
		t.Fatal("incomplete usage changed the ordinary snapshot")
	}
	second := newObservationPersistManager(t, store, "partial-only")
	if err := second.RestoreCooldownStates(ctx); err != nil {
		t.Fatal(err)
	}
	second.SetConfig(reserveTestConfig("claude"))
	restored, _ := second.GetByID("partial-only")
	restored.Metadata = map[string]any{"email": "personal@example.com", "access_token": "synthetic-token"}
	if !second.quotaReserveReached(restored, now) {
		t.Fatal("unknown active usage admitted the protected account")
	}
	if second.quotaReserveReached(restored, reset.Add(time.Second)) {
		t.Fatal("persisted reserve-only reset did not release the account")
	}
}

func TestQuotaReserveSeedsLegacySnapshotBeforePartialUsage(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	reset := now.Add(time.Hour)
	store := NewFileCooldownStateStore(t.TempDir())
	legacy := reserveTestAuth("claude", "legacy", "personal@example.com", 10, 98, reset)
	if err := store.Save(ctx, []CooldownStateRecord{{AuthID: "legacy", Provider: "claude", Status: cooldownStateStatusObserved, Quota: legacy.Quota, UpdatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	manager := newObservationPersistManager(t, store, "legacy")
	if err := manager.RestoreCooldownStates(ctx); err != nil {
		t.Fatal(err)
	}
	manager.SetConfig(reserveTestConfig("claude"))
	headers := reserveHeaders(legacy.Quota.Signals)
	headers.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.5")
	headers.Del("Anthropic-Ratelimit-Unified-7d-Reset")
	manager.ObserveQuotaHeaders("legacy", headers)
	observed, _ := manager.GetByID("legacy")
	observed.Metadata = legacy.Metadata
	if !manager.quotaReserveReached(observed, now) {
		t.Fatal("first partial usage read erased the legacy 98-percent weekly reading")
	}
}
