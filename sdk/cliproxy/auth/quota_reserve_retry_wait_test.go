package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type reserveRetryExecutor struct {
	compactTestExecutor
	mu         sync.Mutex
	fail       bool
	calls      map[string]int
	dispatched []time.Time
}

func (e *reserveRetryExecutor) Identifier() string { return "codex" }

func (e *reserveRetryExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls[auth.ID]++
	e.dispatched = append(e.dispatched, time.Now())
	if e.fail {
		return cliproxyexecutor.Response{}, compactTestStatusError{code: http.StatusServiceUnavailable, msg: "upstream connect error"}
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func registerReserveRetryAuths(t *testing.T, m *Manager, model string, auths ...*Auth) {
	t.Helper()
	for _, auth := range auths {
		if _, err := m.Register(WithSkipPersist(context.Background()), auth); err != nil {
			t.Fatal(err)
		}
		id := auth.ID
		registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
}

func TestExecuteWaitsForCooldownWhenRemainingCredentialIsReserved(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		SetTransientErrorCooldownSeconds(1)
		t.Cleanup(func() { SetTransientErrorCooldownSeconds(0) })
		executor := &reserveRetryExecutor{fail: true, calls: map[string]int{}}
		m := NewManager(nil, nil, nil)
		affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &SoonestResetSelector{}, TTL: time.Hour})
		defer affinity.Stop()
		m.SetSelector(affinity)
		m.SetConfig(reserveTestConfig("codex"))
		m.SetRetryConfig(1, 10*time.Second, 2)
		m.RegisterExecutor(executor)
		model := "gpt-reserve-retry-wait"
		now := time.Now()
		registerReserveRetryAuths(t, m, model,
			reserveTestAuth("codex", "usable-1", "one@example.com", 10, 10, now.Add(time.Hour)),
			reserveTestAuth("codex", "usable-2", "two@example.com", 10, 10, now.Add(2*time.Hour)),
			reserveTestAuth("codex", "reserved", "personal@example.com", 90, 90, now.Add(3*time.Hour)),
		)
		req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"input":"hi"}`)}

		if _, err := m.Execute(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{}); err == nil {
			t.Fatal("first request succeeded, want upstream failure to seed cooldowns")
		}
		seeded := time.Now()
		var recovery time.Time
		for _, id := range []string{"usable-1", "usable-2"} {
			auth, _ := m.GetByID(id)
			blocked, _, next := isAuthBlockedForModel(auth, model, seeded)
			if !blocked || next.IsZero() {
				t.Fatalf("%s blocked = %t next = %v, want a transient cooldown", id, blocked, next)
			}
			if recovery.IsZero() || next.Before(recovery) {
				recovery = next
			}
		}

		executor.mu.Lock()
		executor.fail = false
		executor.dispatched = nil
		executor.mu.Unlock()
		if _, err := m.Execute(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{}); err != nil {
			t.Fatalf("request during transient cooldown failed instead of waiting: %v", err)
		}

		executor.mu.Lock()
		defer executor.mu.Unlock()
		if len(executor.dispatched) == 0 || executor.dispatched[0].Before(recovery) {
			t.Fatalf("second request dispatched at %v, want no earlier than recovery %v", executor.dispatched, recovery)
		}
		if calls := executor.calls["reserved"]; calls != 0 {
			t.Fatalf("reserved credential executor calls = %d, want 0", calls)
		}
	})
}

func TestShouldRetryAfterErrorIgnoresReservedCredentials(t *testing.T) {
	now := time.Now()
	model := "gpt-reserve-retry-decision"
	err := &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "upstream connect error"}
	cases := []struct {
		name   string
		auths  []*Auth
		pinned string
	}{
		{
			name:  "every credential reserved",
			auths: []*Auth{reserveTestAuth("codex", "reserved", "personal@example.com", 90, 90, now.Add(time.Hour))},
		},
		{
			name: "pinned to reserved credential",
			auths: []*Auth{
				reserveTestAuth("codex", "reserved", "personal@example.com", 90, 90, now.Add(time.Hour)),
				reserveTestAuth("codex", "ready", "one@example.com", 10, 10, now.Add(time.Hour)),
			},
			pinned: "reserved",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			m.SetConfig(reserveTestConfig("codex"))
			registerReserveRetryAuths(t, m, model, tc.auths...)
			opts := cliproxyexecutor.Options{}
			if tc.pinned != "" {
				opts.Metadata = map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: tc.pinned}
			}
			wait, retry := m.shouldRetryAfterErrorWithAttempted(context.Background(), opts, err, 0, []string{"codex"}, model, 10*time.Second, -1, 1, nil)
			if retry {
				t.Fatalf("shouldRetryAfterErrorWithAttempted() = (%v, true), want no retry", wait)
			}
		})
	}
}
