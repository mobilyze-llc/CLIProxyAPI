package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func newCredentialQuotaReleaseManager(t *testing.T, provider string) (*Manager, *Auth) {
	t.Helper()
	SetQuotaCooldownDisabled(false)
	manager := NewManager(nil, nil, nil)
	const id = "release-auth"
	registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: "opus"}, {ID: "haiku"}, {ID: "sonnet"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: id, Provider: provider, Status: StatusActive}); errRegister != nil {
		t.Fatal(errRegister)
	}
	retry := 4 * 24 * time.Hour
	manager.MarkResult(context.Background(), Result{AuthID: id, Provider: provider, Model: "opus", CredentialScope: true, RetryAfter: &retry,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"}})
	read, _ := manager.GetByID(id)
	if read.Quota.Reason != "credential_quota" {
		t.Fatalf("seed quota = %+v, want credential_quota", read.Quota)
	}
	return manager, read
}

func TestReleaseCredentialQuotaRefusals(t *testing.T) {
	available := QuotaUsage{CredentialQuotaAvailable: true}
	for _, tc := range []struct {
		name     string
		provider string
		usage    QuotaUsage
		mutate   func(live, read *Auth)
	}{
		{name: "no proof", provider: "claude", usage: QuotaUsage{}},
		{name: "not claude", provider: "codex", usage: available},
		{name: "generation changed", provider: "claude", usage: available, mutate: func(live, _ *Auth) { live.Generation++ }},
		{name: "re-registered", provider: "claude", usage: available, mutate: func(live, _ *Auth) { live.RegistrationEpoch++ }},
		{name: "disabled", provider: "claude", usage: available, mutate: func(live, read *Auth) {
			live.Disabled, live.Status = true, StatusDisabled
			read.Generation = live.Generation
		}},
		{name: "unauthorized", provider: "claude", usage: available, mutate: func(live, read *Auth) {
			live.LastError = &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"}
			read.Generation = live.Generation
		}},
		{name: "invalid grant", provider: "claude", usage: available, mutate: func(live, read *Auth) {
			live.LastError = &Error{HTTPStatus: http.StatusBadRequest, Code: "invalid_grant", Message: "invalid_grant"}
			read.Generation = live.Generation
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, read := newCredentialQuotaReleaseManager(t, tc.provider)
			if tc.mutate != nil {
				manager.mu.Lock()
				tc.mutate(manager.auths[read.ID], read)
				manager.mu.Unlock()
			}
			before, _ := manager.GetByID(read.ID)
			if manager.ReleaseCredentialQuota(read, tc.usage) {
				t.Fatal("release reported success")
			}
			after, _ := manager.GetByID(read.ID)
			if after.Quota.Reason != "credential_quota" || after.Disabled != before.Disabled || after.Status != before.Status ||
				(before.LastError == nil) != (after.LastError == nil) {
				t.Fatalf("refused release changed the auth: before %+v/%v/%v, after %+v/%v/%v",
					before.Quota, before.Status, before.LastError, after.Quota, after.Status, after.LastError)
			}
		})
	}
}

func TestReleaseCredentialQuotaRestoresIndependentModelBlocks(t *testing.T) {
	SetQuotaCooldownDisabled(false)
	manager := NewManager(nil, nil, nil)
	const id = "layers-auth"
	registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: "opus"}, {ID: "haiku"}, {ID: "sonnet"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	ctx := context.Background()
	if _, errRegister := manager.Register(WithSkipPersist(ctx), &Auth{ID: id, Provider: "claude", Status: StatusActive}); errRegister != nil {
		t.Fatal(errRegister)
	}
	fail := func(model string, status int, retry time.Duration, credential bool) {
		manager.MarkResult(ctx, Result{AuthID: id, Provider: "claude", Model: model, CredentialScope: credential, RetryAfter: &retry,
			Error: &Error{HTTPStatus: status, Message: "upstream failure"}})
	}
	fail("haiku", http.StatusTooManyRequests, 24*time.Hour, false)
	fail("sonnet", http.StatusNotFound, 12*time.Hour, false)
	fail("opus", http.StatusTooManyRequests, 4*24*time.Hour, true)
	fail("haiku", http.StatusTooManyRequests, 24*time.Hour, false)
	read, _ := manager.GetByID(id)
	haikuRetry, sonnetRetry := read.ModelStates["haiku"].NextRetryAfter, read.ModelStates["sonnet"].NextRetryAfter

	if !manager.ReleaseCredentialQuota(read, QuotaUsage{CredentialQuotaAvailable: true}) {
		t.Fatal("release refused")
	}
	if picked, errPick := manager.scheduler.pickSingle(ctx, "claude", "opus", cliproxyexecutor.Options{}, nil); errPick != nil || picked.ID != id {
		t.Fatalf("scheduler pick for opus = %v, %v", picked, errPick)
	}
	after, _ := manager.GetByID(id)
	if after.Quota.Reason == "credential_quota" {
		t.Fatalf("auth quota after release = %+v", after.Quota)
	}
	for model, want := range map[string]time.Time{"haiku": haikuRetry, "sonnet": sonnetRetry} {
		state := after.ModelStates[model]
		if state == nil || !state.Unavailable || state.Quota.Reason == "credential_quota" || state.NextRetryAfter.After(want) ||
			!state.NextRetryAfter.After(time.Now().Add(time.Hour)) {
			t.Errorf("%s state after release = %+v, want its own block kept", model, state)
		}
		if _, errPick := manager.scheduler.pickSingle(ctx, "claude", model, cliproxyexecutor.Options{}, nil); errPick == nil {
			t.Errorf("scheduler picked %s despite its own block", model)
		}
	}
}
