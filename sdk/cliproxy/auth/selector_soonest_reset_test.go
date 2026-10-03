package auth

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const day = 24 * time.Hour

func unixString(at time.Time) string {
	return strconv.FormatInt(at.Unix(), 10)
}

func claudeAuthResettingAt(id string, resetAt time.Time) *Auth {
	return &Auth{
		ID:       id,
		Provider: "claude",
		Quota: QuotaState{
			ObservedAt: resetAt.Add(-time.Hour),
			Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-7d-Reset": unixString(resetAt),
			},
		},
	}
}

// claudeAuthWithWindows observes a 5-hour window and a weekly window at 50% use.
func claudeAuthWithWindows(id string, fiveHourUtilization float64, fiveHourReset, weekReset time.Time) *Auth {
	auth := claudeAuthResettingAt(id, weekReset)
	auth.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Utilization"] = "0.5"
	auth.Quota.Signals["Anthropic-Ratelimit-Unified-5h-Utilization"] = strconv.FormatFloat(fiveHourUtilization, 'f', -1, 64)
	auth.Quota.Signals["Anthropic-Ratelimit-Unified-5h-Reset"] = unixString(fiveHourReset)
	return auth
}

func codexWindowSignals(position string, minutes int, usedPercent float64, resetAt time.Time) map[string]string {
	prefix := "X-Codex-" + position + "-"
	return map[string]string{
		prefix + "Window-Minutes": strconv.Itoa(minutes),
		prefix + "Used-Percent":   strconv.FormatFloat(usedPercent, 'f', -1, 64),
		prefix + "Reset-At":       unixString(resetAt),
	}
}

func codexAuthWithSignals(id string, signalSets ...map[string]string) *Auth {
	signals := map[string]string{}
	for _, set := range signalSets {
		for key, value := range set {
			signals[key] = value
		}
	}
	return &Auth{ID: id, Provider: "codex", Quota: QuotaState{ObservedAt: time.Now(), Signals: signals}}
}

// codexWeeklyAuth observes a Codex Pro account, whose weekly window is the primary one.
func codexWeeklyAuth(id string, usedPercent float64, resetAt time.Time) *Auth {
	return codexAuthWithSignals(id, codexWindowSignals("Primary", weeklyWindowMinutes, usedPercent, resetAt))
}

func pickSoonestReset(t *testing.T, provider string, auths ...*Auth) string {
	t.Helper()
	got, err := (&SoonestResetSelector{}).Pick(context.Background(), provider, "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	return got.ID
}

func TestSoonestResetSelectorPick_PrefersSoonestWeeklyReset(t *testing.T) {
	t.Parallel()

	now := time.Now()
	if got := pickSoonestReset(t, "claude",
		claudeAuthResettingAt("a", now.Add(5*day)),
		claudeAuthResettingAt("b", now.Add(2*time.Hour)),
		claudeAuthResettingAt("c", now.Add(3*day)),
	); got != "b" {
		t.Fatalf("Pick() = %q, want b", got)
	}
}

// S1: Codex order follows the weekly window, which is chosen by length, not position.
func TestSoonestResetSelectorPick_CodexWeeklyWindowByLength(t *testing.T) {
	t.Parallel()

	now := time.Now()
	if got := pickSoonestReset(t, "codex",
		codexWeeklyAuth("a", 50, now.Add(6*day)),
		codexWeeklyAuth("b", 50, now.Add(1*day)),
		codexWeeklyAuth("c", 50, now.Add(3*day)),
	); got != "b" {
		t.Fatalf("Pick() = %q, want b", got)
	}

	// x's 5-hour primary window resets first, but y's weekly window resets before x's.
	x := codexAuthWithSignals("x",
		codexWindowSignals("Primary", 300, 10, now.Add(time.Hour)),
		codexWindowSignals("Secondary", weeklyWindowMinutes, 50, now.Add(5*day)),
	)
	y := codexAuthWithSignals("y", codexWindowSignals("Secondary", weeklyWindowMinutes, 50, now.Add(2*day)))
	if got := pickSoonestReset(t, "codex", x, y); got != "y" {
		t.Fatalf("Pick() = %q, want y", got)
	}
}

// S2: a small remainder resetting soon is used before a fresh window resetting late.
func TestSoonestResetSelectorPick_SmallRemainderResettingSoonFirst(t *testing.T) {
	t.Parallel()

	now := time.Now()
	if got := pickSoonestReset(t, "codex",
		codexWeeklyAuth("a", 0, now.Add(5*day)),
		codexWeeklyAuth("b", 98, now.Add(6*time.Hour)),
	); got != "b" {
		t.Fatalf("Pick() = %q, want b", got)
	}
}

func TestSoonestResetSelectorPick_SkipsCoolingDownAuth(t *testing.T) {
	t.Parallel()

	now := time.Now()
	exhausted := claudeAuthResettingAt("b", now.Add(2*time.Hour))
	exhausted.Quota.Exceeded = true
	exhausted.Quota.Reason = "credential_quota"
	exhausted.Quota.NextRecoverAt = now.Add(2 * time.Hour)
	if got := pickSoonestReset(t, "claude",
		claudeAuthResettingAt("a", now.Add(5*day)),
		exhausted,
		claudeAuthResettingAt("c", now.Add(3*day)),
	); got != "c" {
		t.Fatalf("Pick() = %q, want c", got)
	}
}

// S3: an exhausted window skips the credential until that window resets.
func TestSoonestResetSelectorPick_ExhaustionGate(t *testing.T) {
	t.Parallel()

	now := time.Now()
	later := claudeAuthWithWindows("b", 0.1, now.Add(4*time.Hour), now.Add(3*day))
	rejected := claudeAuthWithWindows("a", 0.5, now.Add(time.Hour), now.Add(day))
	rejected.Quota.Signals["Anthropic-Ratelimit-Unified-5h-Status"] = "rejected"
	limitReached := codexAuthWithSignals("a",
		codexWindowSignals("Primary", 300, 90, now.Add(2*time.Hour)),
		codexWindowSignals("Secondary", weeklyWindowMinutes, 40, now.Add(day)),
		map[string]string{"X-Codex-Limit-Reached": "true"},
	)

	tests := []struct {
		name     string
		provider string
		auths    []*Auth
		want     string
	}{
		{
			name:     "claude 5h utilization",
			provider: "claude",
			auths:    []*Auth{claudeAuthWithWindows("a", 1, now.Add(time.Hour), now.Add(day)), later},
			want:     "b",
		},
		{name: "claude 5h rejected", provider: "claude", auths: []*Auth{rejected, later}, want: "b"},
		{
			name:     "claude 5h reset passed",
			provider: "claude",
			auths:    []*Auth{claudeAuthWithWindows("a", 1, now.Add(-time.Minute), now.Add(day)), later},
			want:     "a",
		},
		{
			name:     "every candidate gated keeps the ordering",
			provider: "claude",
			auths: []*Auth{
				claudeAuthWithWindows("a", 1, now.Add(time.Hour), now.Add(day)),
				claudeAuthWithWindows("b", 1, now.Add(time.Hour), now.Add(3*day)),
			},
			want: "a",
		},
		{
			name:     "codex used 100",
			provider: "codex",
			auths:    []*Auth{codexWeeklyAuth("a", 100, now.Add(day)), codexWeeklyAuth("b", 10, now.Add(3*day))},
			want:     "b",
		},
		{
			name:     "codex limit reached",
			provider: "codex",
			auths:    []*Auth{limitReached, codexWeeklyAuth("b", 10, now.Add(3*day))},
			want:     "b",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pickSoonestReset(t, tt.provider, tt.auths...); got != tt.want {
				t.Fatalf("Pick() = %q, want %q", got, tt.want)
			}
		})
	}
}

// A Codex window without a Window-Minutes header still gates the credential when exhausted.
func TestSoonestResetSelectorPick_CodexWindowWithoutLengthStillGates(t *testing.T) {
	t.Parallel()

	now := time.Now()
	primary := codexWindowSignals("Primary", 0, 100, now.Add(time.Hour))
	delete(primary, "X-Codex-Primary-Window-Minutes")
	a := codexAuthWithSignals("a", primary, codexWindowSignals("Secondary", weeklyWindowMinutes, 40, now.Add(day)))
	if got := pickSoonestReset(t, "codex", a, codexWeeklyAuth("b", 10, now.Add(3*day))); got != "b" {
		t.Fatalf("Pick() = %q, want b", got)
	}
}

// S5: an unknown Claude reset sorts first, an unknown or passed Codex reset sorts last, and a
// passed Claude reset rolls forward by whole weeks.
func TestSoonestResetSelectorPick_UnknownResets(t *testing.T) {
	t.Parallel()

	now := time.Now()
	if got := pickSoonestReset(t, "codex",
		&Auth{ID: "a", Provider: "codex"},
		codexWeeklyAuth("b", 50, now.Add(-time.Hour)),
		codexWeeklyAuth("c", 50, now.Add(6*day)),
	); got != "c" {
		t.Fatalf("codex Pick() = %q, want c", got)
	}
	if got := pickSoonestReset(t, "claude",
		claudeAuthResettingAt("a", now.Add(time.Hour)),
		&Auth{ID: "b", Provider: "claude"},
	); got != "b" {
		t.Fatalf("claude Pick() = %q, want b", got)
	}
	// a's reset passed six days ago, so its next one is in one day.
	if got := pickSoonestReset(t, "claude",
		claudeAuthResettingAt("a", now.Add(2*day)),
		claudeAuthResettingAt("b", now.Add(-6*day)),
	); got != "b" {
		t.Fatalf("claude rolled-forward Pick() = %q, want b", got)
	}
	_, rolled := soonestResetRank(claudeAuthResettingAt("b", now.Add(-6*day)), now)
	if want := time.Unix(now.Add(-6*day).Unix(), 0).Add(week); !rolled.Equal(want) {
		t.Fatalf("rolled reset = %v, want %v", rolled, want)
	}
}

func newSoonestResetManager(t *testing.T, store CooldownStateStore, auths ...*Auth) *Manager {
	t.Helper()
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)
	affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &SoonestResetSelector{}, TTL: time.Hour})
	t.Cleanup(affinity.Stop)
	manager.SetSelector(affinity)
	manager.SetCooldownStateStore(store)
	manager.RegisterExecutor(schedulerTestExecutor{provider: "codex"})
	for _, auth := range auths {
		if _, err := manager.Register(WithSkipPersist(ctx), auth); err != nil {
			t.Fatalf("Register(%s): %v", auth.ID, err)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "soonest-reset-model"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}
	return manager
}

func pickSession(t *testing.T, manager *Manager, session string) string {
	t.Helper()
	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: session}}
	auth, _, err := manager.pickNext(context.Background(), "codex", "soonest-reset-model", opts, nil)
	if err != nil {
		t.Fatalf("pickNext() error = %v", err)
	}
	return auth.ID
}

func markWithHeaders(manager *Manager, authID string, headers http.Header) {
	ctx := internallogging.WithResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, headers)
	manager.MarkResult(ctx, Result{AuthID: authID, Provider: "codex", Model: "soonest-reset-model", Success: true})
}

func codexWeeklyHeaders(resetAt time.Time) http.Header {
	headers := http.Header{}
	for key, value := range codexWindowSignals("Primary", weeklyWindowMinutes, 50, resetAt) {
		headers.Set(key, value)
	}
	return headers
}

// Routing reaches the selector through session affinity, and a bound session stays put.
func TestSoonestResetSelector_RoutedThroughSessionAffinity(t *testing.T) {
	now := time.Now()
	manager := newSoonestResetManager(t, nil,
		codexWeeklyAuth("soonest-routing-a", 50, now.Add(3*day)),
		codexWeeklyAuth("soonest-routing-b", 50, now.Add(day)),
	)
	if manager.useSchedulerFastPath() {
		t.Fatal("session affinity must use the legacy pick path")
	}
	if got := pickSession(t, manager, "first"); got != "soonest-routing-b" {
		t.Fatalf("new session = %q, want soonest-routing-b", got)
	}

	markWithHeaders(manager, "soonest-routing-a", codexWeeklyHeaders(now.Add(time.Hour)))
	if got := pickSession(t, manager, "first"); got != "soonest-routing-b" {
		t.Fatalf("bound session = %q, want soonest-routing-b", got)
	}
	if got := pickSession(t, manager, "second"); got != "soonest-routing-a" {
		t.Fatalf("new session after observation = %q, want soonest-routing-a", got)
	}
}

// S10: observations saved before a restart order new sessions after it, with no new headers.
func TestSoonestResetSelector_PersistedObservationsSurviveRestart(t *testing.T) {
	store := NewFileCooldownStateStore(t.TempDir())
	now := time.Now()
	ids := []string{"soonest-restart-a", "soonest-restart-b"}
	resets := []time.Time{now.Add(6 * day), now.Add(day)}
	fresh := func() []*Auth {
		auths := make([]*Auth, len(ids))
		for i, id := range ids {
			auths[i] = &Auth{ID: id, Provider: "codex", Status: StatusActive}
		}
		return auths
	}

	first := newSoonestResetManager(t, store, fresh()...)
	for i, id := range ids {
		markWithHeaders(first, id, codexWeeklyHeaders(resets[i]))
	}

	second := newSoonestResetManager(t, store, fresh()...)
	if got := pickSession(t, second, "before-restore"); got != ids[0] {
		t.Fatalf("pick with no observations = %q, want %q (ID order)", got, ids[0])
	}
	if err := second.RestoreCooldownStates(context.Background()); err != nil {
		t.Fatalf("RestoreCooldownStates() error = %v", err)
	}
	if got := pickSession(t, second, "after-restore"); got != ids[1] {
		t.Fatalf("pick after restore = %q, want %q", got, ids[1])
	}
}

// Quota signals written by MarkResult and read by the selector do not race.
func TestSoonestResetSelector_ConcurrentObservationAndPick(t *testing.T) {
	now := time.Now()
	manager := newSoonestResetManager(t, nil,
		&Auth{ID: "soonest-race-a", Provider: "codex", Status: StatusActive},
		&Auth{ID: "soonest-race-b", Provider: "codex", Status: StatusActive},
	)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				markWithHeaders(manager, "soonest-race-a", codexWeeklyHeaders(now.Add(time.Duration(j)*time.Hour)))
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: strconv.Itoa(j)}}
				if _, _, err := manager.pickNext(context.Background(), "codex", "soonest-reset-model", opts, nil); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
}
