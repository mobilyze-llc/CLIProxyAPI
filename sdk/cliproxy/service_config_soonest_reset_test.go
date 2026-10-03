package cliproxy

import (
	"context"
	"strconv"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestSoonestResetRoutingSelectorWrappedBySessionAffinity(t *testing.T) {
	selector := newRoutingSelector(normalizedRoutingRuntimeState(&internalconfig.Config{
		Routing: internalconfig.RoutingConfig{Strategy: "soonest-reset", SessionAffinity: true},
	}))
	affinity, ok := selector.(*coreauth.SessionAffinitySelector)
	if !ok {
		t.Fatalf("selector type = %T, want *auth.SessionAffinitySelector", selector)
	}
	defer affinity.Stop()

	weekly := func(id string, resetAt time.Time) *coreauth.Auth {
		return &coreauth.Auth{ID: id, Provider: "codex", Quota: coreauth.QuotaState{
			ObservedAt: time.Now(),
			Signals: map[string]string{
				"X-Codex-Primary-Window-Minutes": "10080",
				"X-Codex-Primary-Reset-At":       strconv.FormatInt(resetAt.Unix(), 10),
			},
		}}
	}
	auths := []*coreauth.Auth{weekly("a", time.Now().Add(72*time.Hour)), weekly("b", time.Now().Add(time.Hour))}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "s"}}
	got, err := affinity.Pick(context.Background(), "codex", "m", opts, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "b" {
		t.Fatalf("Pick() = %q, want b", got.ID)
	}
}
