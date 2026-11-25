package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func reserveHeaders(signals map[string]string) http.Header {
	headers := make(http.Header, len(signals))
	for name, value := range signals {
		headers.Set(name, value)
	}
	return headers
}

func TestQuotaReserveHistoryPreservesEachWindowUntilReset(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	reset := now.Add(time.Hour)
	for _, provider := range []string{"claude", "codex"} {
		for _, limiting := range []string{"five-hour", "weekly"} {
			for _, partial := range []string{"missing", "hollow-zero", "unanchored-low", "malformed"} {
				t.Run(provider+"/"+limiting+"/"+partial, func(t *testing.T) {
					manager := NewManager(nil, nil, nil)
					manager.SetConfig(reserveTestConfig(provider))
					auth := reserveTestAuth(provider, "protected", "personal@example.com", 10, 10, reset)
					high := reserveTestAuth(provider, "source", "", 10, 95, reset)
					if limiting == "five-hour" {
						high = reserveTestAuth(provider, "source", "", 95, 10, reset)
					}
					auth.Quota.ObservedAt = now.Add(-time.Minute)
					auth.Quota.ObserveResponseHeadersForProvider(provider, reserveHeaders(high.Quota.Signals), now)
					prefix, usage, resetName := "Anthropic-Ratelimit-Unified-7d-", "Utilization", "Reset"
					if limiting == "five-hour" {
						prefix = "Anthropic-Ratelimit-Unified-5h-"
					}
					if provider == "codex" {
						prefix, usage, resetName = "X-Codex-Primary-", "Used-Percent", "Reset-At"
						if limiting == "five-hour" {
							prefix = "X-Codex-Secondary-"
						}
					}
					headers := reserveHeaders(reserveTestAuth(provider, "source", "", 10, 10, reset).Quota.Signals)
					headers.Del(prefix + resetName)
					switch partial {
					case "missing":
						headers.Del(prefix + usage)
					case "hollow-zero":
						headers.Set(prefix+usage, "0")
					case "malformed":
						headers.Set(prefix+usage, "unknown")
					}
					auth.Quota.ObserveResponseHeadersForProvider(provider, headers, now.Add(time.Minute))
					if !manager.quotaReserveReached(auth, now.Add(time.Minute)) {
						t.Fatal("partial snapshot cleared a protected window")
					}
					if manager.quotaReserveReached(auth, reset.Add(time.Second)) {
						t.Fatal("missing snapshot lost the known reset and prevented recovery")
					}
					low := reserveTestAuth(provider, "source", "", 10, 10, reset)
					auth.Quota.ObserveResponseHeadersForProvider(provider, reserveHeaders(low.Quota.Signals), now.Add(2*time.Minute))
					if manager.quotaReserveReached(auth, now.Add(2*time.Minute)) {
						t.Fatal("valid lower reading did not clear the reserve block")
					}
				})
			}
		}
	}
}

func TestQuotaReserveHistorySurvivesRestartAndAuthRefresh(t *testing.T) {
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			store := NewFileCooldownStateStore(t.TempDir())
			makeManager := func() *Manager {
				manager := NewManager(nil, nil, nil)
				manager.SetConfig(reserveTestConfig(provider))
				manager.SetCooldownStateStore(store)
				if _, err := manager.Register(ctx, &Auth{ID: "protected", Provider: provider, Status: StatusActive, Metadata: map[string]any{"email": "personal@example.com", "access_token": "synthetic-token"}}); err != nil {
					t.Fatal(err)
				}
				return manager
			}
			first := makeManager()
			high := reserveTestAuth(provider, "source", "", 95, 10, now.Add(time.Hour))
			first.ObserveQuotaHeaders("protected", reserveHeaders(high.Quota.Signals))
			partial := http.Header{"Anthropic-Ratelimit-Unified-7d-Utilization": {"0.1"}, "Anthropic-Ratelimit-Unified-7d-Reset": {unixString(now.Add(time.Hour))}}
			if provider == "codex" {
				partial = reserveHeaders(codexWindowSignals("Primary", weeklyWindowMinutes, 10, now.Add(time.Hour)))
			}
			first.ObserveQuotaHeaders("protected", partial)
			second := makeManager()
			if err := second.RestoreCooldownStates(ctx); err != nil {
				t.Fatal(err)
			}
			fresh := &Auth{ID: "protected", Provider: provider, Status: StatusActive, Metadata: map[string]any{"email": "personal@example.com", "access_token": "refreshed-synthetic-token"}}
			if _, err := second.Update(WithSkipPersist(ctx), fresh); err != nil {
				t.Fatal(err)
			}
			restored, _ := second.GetByID("protected")
			if !second.quotaReserveReached(restored, now) {
				t.Fatal("restart or credential refresh erased the retained five-hour block")
			}
			clone := restored.Clone()
			for name := range clone.Quota.ReserveWindows {
				delete(clone.Quota.ReserveWindows, name)
			}
			if !second.quotaReserveReached(restored, now) {
				t.Fatal("cloning shared mutable reserve state")
			}
			if second.quotaReserveReached(restored, now.Add(2*time.Hour)) {
				t.Fatal("persisted reset no longer releases the account")
			}
		})
	}
}

func TestQuotaReserveHistoryGlobalRejectionAndNewWindow(t *testing.T) {
	now := time.Now()
	for _, provider := range []string{"claude", "codex"} {
		manager := NewManager(nil, nil, nil)
		manager.SetConfig(reserveTestConfig(provider))
		auth := reserveTestAuth(provider, "protected", "personal@example.com", 10, 10, now.Add(time.Hour))
		auth.Quota.ObservedAt = now.Add(-time.Minute)
		auth.Quota.ObserveResponseHeadersForProvider(provider, reserveHeaders(auth.Quota.Signals), now)
		rejected := http.Header{"Anthropic-Ratelimit-Unified-5h-Status": {"rejected"}}
		if provider == "codex" {
			rejected = http.Header{"X-Codex-Limit-Reached": {"true"}}
		}
		auth.Quota.ObserveResponseHeadersForProvider(provider, rejected, now.Add(time.Minute))
		if !manager.quotaReserveReached(auth, now.Add(time.Minute)) {
			t.Fatalf("%s rejection was lost behind earlier low usage", provider)
		}
		high := reserveTestAuth(provider, "source", "", 95, 10, time.Time{})
		headers := reserveHeaders(high.Quota.Signals)
		for name := range headers {
			if name == "Anthropic-Ratelimit-Unified-5h-Reset" || name == "Anthropic-Ratelimit-Unified-7d-Reset" || name == "X-Codex-Primary-Reset-At" || name == "X-Codex-Secondary-Reset-At" {
				headers.Del(name)
			}
		}
		auth.Quota.ObserveResponseHeadersForProvider(provider, headers, now.Add(2*time.Hour))
		if !manager.quotaReserveReached(auth, now.Add(2*time.Hour)) {
			t.Fatalf("%s new high reading inherited an expired reset", provider)
		}
	}
}

func TestQuotaReserveHistoryCodexInactiveSecondary(t *testing.T) {
	now := time.Now()
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(reserveTestConfig("codex"))
	for _, relativeReset := range []string{"", "0"} {
		auth := reserveTestAuth("codex", "protected", "personal@example.com", 0, 10, now.Add(time.Hour))
		auth.Quota = QuotaState{}
		headers := reserveHeaders(codexWindowSignals("Primary", weeklyWindowMinutes, 10, now.Add(time.Hour)))
		headers.Set("X-Codex-Secondary-Used-Percent", "0")
		headers.Set("X-Codex-Secondary-Window-Minutes", "0")
		if relativeReset != "" {
			headers.Set("X-Codex-Secondary-Reset-After-Seconds", relativeReset)
		}
		auth.Quota.ObserveResponseHeadersForProvider("codex", headers, now)
		if manager.quotaReserveReached(auth, now.Add(time.Second)) {
			t.Fatalf("inactive secondary with relative reset %q excluded weekly-only plan", relativeReset)
		}
		auth.Quota.ObserveResponseHeadersForProvider("codex", http.Header{"X-Codex-Limit-Reached": {"true"}}, now.Add(time.Minute))
		if !manager.quotaReserveReached(auth, now.Add(time.Minute)) {
			t.Fatal("credential-wide rejection did not exclude the account")
		}
		healthy := reserveHeaders(codexWindowSignals("Primary", weeklyWindowMinutes, 10, now.Add(time.Hour)))
		auth.Quota.ObserveResponseHeadersForProvider("codex", healthy, now.Add(2*time.Minute))
		if manager.quotaReserveReached(auth, now.Add(2*time.Minute)) {
			t.Fatalf("inactive secondary with relative reset %q survived as a phantom block", relativeReset)
		}
	}
}

func TestHasAlternativeTargetForModelExcludesReservedCredentials(t *testing.T) {
	ctx := WithSkipPersist(context.Background())
	now := time.Now()
	manager := NewManager(nil, &SoonestResetSelector{}, nil)
	manager.SetConfig(reserveTestConfig("codex"))
	manager.RegisterExecutor(schedulerTestExecutor{provider: "codex"})
	primary := reserveTestAuth("codex", "primary", "other@example.com", 10, 10, now.Add(time.Hour))
	reserved := reserveTestAuth("codex", "reserved", "personal@example.com", 95, 10, now.Add(time.Hour))
	if _, err := manager.Register(ctx, primary); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Register(ctx, reserved); err != nil {
		t.Fatal(err)
	}
	if manager.HasAlternativeTargetForModel(ctx, []string{"codex"}, "", cliproxyexecutor.Options{}, "primary") {
		t.Fatal("HasAlternativeTargetForModel treated a quota-reserved credential as an available failover target")
	}
	available := reserveTestAuth("codex", "available", "third@example.com", 10, 10, now.Add(time.Hour))
	if _, err := manager.Register(ctx, available); err != nil {
		t.Fatal(err)
	}
	if !manager.HasAlternativeTargetForModel(ctx, []string{"codex"}, "", cliproxyexecutor.Options{}, "primary") {
		t.Fatal("HasAlternativeTargetForModel missed an unreserved failover target")
	}
}

func TestCredentialLevelFailurePreservesQuotaReserveHistory(t *testing.T) {
	ctx := WithSkipPersist(context.Background())
	now := time.Now().Truncate(time.Second)
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(reserveTestConfig("codex"))
	auth := reserveTestAuth("codex", "protected", "personal@example.com", 95, 10, now.Add(time.Hour))
	auth.Quota.ObservedAt = now
	auth.Quota.ObserveResponseHeadersForProvider("codex", reserveHeaders(auth.Quota.Signals), now)
	auth.ModelStates = map[string]*ModelState{"gpt-6.2-sol": {Status: StatusActive}}
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatal(err)
	}
	primaryOnly := reserveHeaders(codexWindowSignals("Primary", weeklyWindowMinutes, 10, now.Add(time.Hour)))
	markCtx := internallogging.WithResponseHeadersHolder(ctx)
	internallogging.SetResponseHeaders(markCtx, primaryOnly)
	manager.MarkResult(markCtx, Result{
		AuthID:          "protected",
		Provider:        "codex",
		CredentialScope: true,
		Success:         false,
		Error:           &Error{HTTPStatus: http.StatusInternalServerError, Message: "transient"},
	})
	after, _ := manager.GetByID("protected")
	if !manager.quotaReserveReached(after, now.Add(time.Minute)) {
		t.Fatal("credential-scoped failure erased retained secondary reserve window")
	}
}

func TestRefreshWithOlderNonZeroObservationDoesNotRollBackReserveEvidence(t *testing.T) {
	ctx := WithSkipPersist(context.Background())
	now := time.Now().Truncate(time.Second)
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(reserveTestConfig("codex"))
	initial := reserveTestAuth("codex", "protected", "personal@example.com", 10, 10, now.Add(time.Hour))
	initial.Quota.ObservedAt = now
	initial.Quota.ObserveResponseHeadersForProvider("codex", reserveHeaders(initial.Quota.Signals), now)
	if _, err := manager.Register(ctx, initial); err != nil {
		t.Fatal(err)
	}
	refreshSnapshot, _ := manager.GetByID("protected")
	high := reserveTestAuth("codex", "source", "", 95, 10, now.Add(time.Hour))
	manager.ObserveQuotaHeadersAt("protected", reserveHeaders(high.Quota.Signals), now.Add(time.Minute))
	refreshSnapshot.Metadata["access_token"] = "refreshed-token"
	if _, err := manager.Update(ctx, refreshSnapshot); err != nil {
		t.Fatal(err)
	}
	after, _ := manager.GetByID("protected")
	if !manager.quotaReserveReached(after, now.Add(2*time.Minute)) {
		t.Fatal("in-flight refresh with older ObservedAt rolled back newer reserve evidence")
	}
	if !after.Quota.ObservedAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("ObservedAt = %v, want %v", after.Quota.ObservedAt, now.Add(time.Minute))
	}
}

func TestSlowUsageReadAndPartialPollsPreserveNewerAndOtherWindows(t *testing.T) {
	ctx := WithSkipPersist(context.Background())
	now := time.Now().Truncate(time.Second)
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(reserveTestConfig("claude"))
	auth := reserveTestAuth("claude", "protected", "personal@example.com", 10, 10, now.Add(time.Hour))
	if _, err := manager.Register(ctx, auth); err != nil {
		t.Fatal(err)
	}
	high := reserveTestAuth("claude", "source", "", 90, 60, now.Add(2*time.Hour))
	manager.ObserveQuotaHeadersAt("protected", reserveHeaders(high.Quota.Signals), now.Add(2*time.Minute))
	staleLow := reserveTestAuth("claude", "source", "", 10, 10, now.Add(time.Hour))
	manager.ObserveQuotaHeadersAt("protected", reserveHeaders(staleLow.Quota.Signals), now.Add(time.Minute))
	afterStale, _ := manager.GetByID("protected")
	if !manager.quotaReserveReached(afterStale, now.Add(3*time.Minute)) {
		t.Fatal("slow usage read stamped at read start rolled back newer reserve window")
	}
	if got := afterStale.Quota.Signals["Anthropic-Ratelimit-Unified-5h-Utilization"]; got != "0.9" {
		t.Fatalf("slow usage read rolled back 5h signal to %q, want 0.9", got)
	}
	partial5hOnly := http.Header{
		"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.2"},
		"Anthropic-Ratelimit-Unified-5h-Reset":       {unixString(now.Add(3 * time.Hour))},
		"Anthropic-Ratelimit-Unified-7d-Utilization": {"0.7"},
	}
	manager.ObserveQuotaHeadersAt("protected", partial5hOnly, now.Add(4*time.Minute))
	afterPartial, _ := manager.GetByID("protected")
	if got := afterPartial.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Reset"]; got != unixString(now.Add(2*time.Hour)) {
		t.Fatalf("partial Claude poll erased prior 7d reset: got %q, want %q", got, unixString(now.Add(2*time.Hour)))
	}
	if got := afterPartial.Quota.Signals["Anthropic-Ratelimit-Unified-5h-Utilization"]; got != "0.2" {
		t.Fatalf("complete 5h window in partial poll did not update: got %q, want 0.2", got)
	}

	codex := reserveTestAuth("codex", "codex-partial", "other@example.com", 30, 40, now.Add(2*time.Hour))
	if _, err := manager.Register(ctx, codex); err != nil {
		t.Fatal(err)
	}
	manager.ObserveQuotaHeadersAt("codex-partial", reserveHeaders(codex.Quota.Signals), now.Add(time.Minute))
	codexPrimaryOnly := reserveHeaders(codexWindowSignals("Primary", weeklyWindowMinutes, 45, now.Add(3*time.Hour)))
	manager.ObserveQuotaHeadersAt("codex-partial", codexPrimaryOnly, now.Add(2*time.Minute))
	afterCodex, _ := manager.GetByID("codex-partial")
	if got := afterCodex.Quota.Signals["X-Codex-Secondary-Reset-At"]; got != unixString(now.Add(2*time.Hour)) {
		t.Fatalf("partial Codex poll erased prior secondary window: got %q", got)
	}
	if got := afterCodex.Quota.Signals["X-Codex-Primary-Used-Percent"]; got != "45" {
		t.Fatalf("complete primary window in Codex poll did not update: got %q, want 45", got)
	}
}
