package auth

import (
	"context"
	"strconv"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func claudeAuthResettingAt(id string, resetAt time.Time) *Auth {
	return &Auth{
		ID:       id,
		Provider: "claude",
		Quota: QuotaState{
			ObservedAt: resetAt.Add(-time.Hour),
			Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-7d-Reset": strconv.FormatInt(resetAt.Unix(), 10),
			},
		},
	}
}

func TestSoonestResetSelectorPick_PrefersSoonestWeeklyReset(t *testing.T) {
	t.Parallel()

	now := time.Now()
	auths := []*Auth{
		claudeAuthResettingAt("a", now.Add(5*24*time.Hour)),
		claudeAuthResettingAt("b", now.Add(2*time.Hour)),
		claudeAuthResettingAt("c", now.Add(3*24*time.Hour)),
	}

	got, err := (&SoonestResetSelector{}).Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "b" {
		t.Fatalf("Pick() auth.ID = %q, want %q", got.ID, "b")
	}
}

func TestSoonestResetSelectorPick_SkipsCoolingDownAuth(t *testing.T) {
	t.Parallel()

	now := time.Now()
	exhausted := claudeAuthResettingAt("b", now.Add(2*time.Hour))
	exhausted.Quota.Exceeded = true
	exhausted.Quota.Reason = "credential_quota"
	exhausted.Quota.NextRecoverAt = now.Add(2 * time.Hour)
	auths := []*Auth{
		claudeAuthResettingAt("a", now.Add(5*24*time.Hour)),
		exhausted,
		claudeAuthResettingAt("c", now.Add(3*24*time.Hour)),
	}

	got, err := (&SoonestResetSelector{}).Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "c" {
		t.Fatalf("Pick() auth.ID = %q, want %q", got.ID, "c")
	}
}
