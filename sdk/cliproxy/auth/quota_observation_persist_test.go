package auth

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func newObservationPersistManager(t *testing.T, store CooldownStateStore, authID string) *Manager {
	t.Helper()
	m := NewManager(nil, nil, nil)
	m.SetCooldownStateStore(store)
	if _, err := m.Register(context.Background(), &Auth{ID: authID, Provider: "claude", Status: StatusActive}); err != nil {
		t.Fatalf("register: %v", err)
	}
	return m
}

func TestQuotaObservationsSurviveRestart(t *testing.T) {
	ctx := context.Background()
	store := NewFileCooldownStateStore(t.TempDir())
	now := time.Now()
	weeklyReset := now.Add(48 * time.Hour).Truncate(time.Second)
	fableReset := now.Add(20 * time.Hour).Truncate(time.Second)

	first := newObservationPersistManager(t, store, "claude-a.json")
	first.mu.Lock()
	auth := first.auths["claude-a.json"]
	auth.Quota.ObservedAt = now.Add(-time.Minute)
	auth.Quota.Signals = map[string]string{
		"Anthropic-Ratelimit-Unified-7d-Reset": strconv.FormatInt(weeklyReset.Unix(), 10),
	}
	ensureModelState(auth, "claude-fable-5").Quota = QuotaState{
		ObservedAt: now.Add(-time.Minute),
		Signals: map[string]string{
			"Anthropic-Ratelimit-Unified-7d_oi-Reset": strconv.FormatInt(fableReset.Unix(), 10),
		},
	}
	first.mu.Unlock()
	first.persistCooldownStates(ctx)

	second := newObservationPersistManager(t, store, "claude-a.json")
	if err := second.RestoreCooldownStates(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	restored, ok := second.GetByID("claude-a.json")
	if !ok {
		t.Fatal("auth missing after restore")
	}
	if got, want := restored.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Reset"], strconv.FormatInt(weeklyReset.Unix(), 10); got != want {
		t.Fatalf("credential weekly reset = %q, want %q", got, want)
	}
	if got, want := restored.ModelStates["claude-fable-5"].Quota.Signals["Anthropic-Ratelimit-Unified-7d_oi-Reset"], strconv.FormatInt(fableReset.Unix(), 10); got != want {
		t.Fatalf("fable weekly reset = %q, want %q", got, want)
	}
	if restored.Unavailable || restored.ModelStates["claude-fable-5"].Unavailable {
		t.Fatal("restoring an observation must not make the auth or model unavailable")
	}
}

func TestQuotaObservationSharesRecordWithCooldown(t *testing.T) {
	ctx := context.Background()
	store := NewFileCooldownStateStore(t.TempDir())
	now := time.Now()
	retryAt := now.Add(time.Hour)

	first := newObservationPersistManager(t, store, "claude-b.json")
	first.mu.Lock()
	state := ensureModelState(first.auths["claude-b.json"], "claude-opus")
	state.Unavailable = true
	state.Status = StatusError
	state.NextRetryAfter = retryAt
	state.Quota = QuotaState{
		Exceeded:   true,
		Reason:     "quota",
		ObservedAt: now,
		Signals:    map[string]string{"Anthropic-Ratelimit-Unified-7d-Utilization": "1.0"},
	}
	first.mu.Unlock()

	records := first.cooldownStateRecordsSnapshot()
	if len(records) != 1 {
		t.Fatalf("records = %+v, want one combined record", records)
	}
	if records[0].NextRetryAfter.IsZero() || records[0].Quota.Signals["Anthropic-Ratelimit-Unified-7d-Utilization"] != "1.0" {
		t.Fatalf("record lost cooldown or observation: %+v", records[0])
	}
	first.persistCooldownStates(ctx)

	second := newObservationPersistManager(t, store, "claude-b.json")
	if err := second.RestoreCooldownStates(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	restored, _ := second.GetByID("claude-b.json")
	got := restored.ModelStates["claude-opus"]
	if got == nil || !got.Unavailable || !got.NextRetryAfter.Equal(retryAt) {
		t.Fatalf("cooldown not restored: %+v", got)
	}
	if got.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Utilization"] != "1.0" {
		t.Fatalf("observation not restored: %+v", got.Quota)
	}
}

func TestStaleQuotaObservationIsNotPersisted(t *testing.T) {
	store := NewFileCooldownStateStore(t.TempDir())
	m := newObservationPersistManager(t, store, "claude-c.json")
	m.mu.Lock()
	m.auths["claude-c.json"].Quota = QuotaState{
		ObservedAt: time.Now().Add(-quotaObservationRetention - time.Hour),
		Signals:    map[string]string{"Anthropic-Ratelimit-Unified-7d-Utilization": "0.5"},
	}
	m.mu.Unlock()

	if records := m.cooldownStateRecordsSnapshot(); len(records) != 0 {
		t.Fatalf("stale observation was persisted: %+v", records)
	}
}

func TestRestoreKeepsNewerLiveObservation(t *testing.T) {
	ctx := context.Background()
	store := NewFileCooldownStateStore(t.TempDir())
	now := time.Now()

	first := newObservationPersistManager(t, store, "claude-d.json")
	first.mu.Lock()
	first.auths["claude-d.json"].Quota = QuotaState{
		ObservedAt: now.Add(-time.Hour),
		Signals:    map[string]string{"Anthropic-Ratelimit-Unified-7d-Utilization": "0.9"},
	}
	first.mu.Unlock()
	first.persistCooldownStates(ctx)

	second := newObservationPersistManager(t, store, "claude-d.json")
	second.mu.Lock()
	second.auths["claude-d.json"].Quota = QuotaState{
		ObservedAt: now,
		Signals:    map[string]string{"Anthropic-Ratelimit-Unified-7d-Utilization": "0.1"},
	}
	second.mu.Unlock()
	if err := second.RestoreCooldownStates(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}
	restored, _ := second.GetByID("claude-d.json")
	if got := restored.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Utilization"]; got != "0.1" {
		t.Fatalf("utilization = %q, want the newer live value 0.1", got)
	}
}
