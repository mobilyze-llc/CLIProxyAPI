package auth

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A disk snapshot applied between an auth-level 429 and a success dispatched before
// that 429 keeps the cooldown's watermark (#6643) along with the cooldown itself, so
// the stale success still does not clear it.
func TestFileSnapshotKeepsCooldownWatermark(t *testing.T) {
	const authID = "snapshot-watermark-auth"
	manager := issue6643SeqTestAuth(t, authID)
	ctx := context.Background()

	staleSeq := manager.nextExecutionSeq()
	manager.MarkResult(ctx, Result{
		AuthID: authID, Provider: "antigravity", Success: false, ExecutionSeq: manager.nextExecutionSeq(),
		Error: &Error{Code: "quota_exhausted", Message: "QUOTA_EXHAUSTED", Retryable: true, HTTPStatus: http.StatusTooManyRequests},
	})
	cooled, _ := manager.GetByID(authID)
	if !cooled.Quota.Exceeded || !cooled.NextRetryAfter.After(time.Now()) || cooled.Quota.Reason == "credential_quota" {
		t.Fatalf("expected an auth-level quota cooldown other than credential_quota, got %+v", cooled)
	}

	path := filepath.Join(t.TempDir(), authID+".json")
	data := []byte(`{"type":"antigravity"}`)
	if errWrite := os.WriteFile(path, data, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	snapshot := &Auth{ID: authID, Provider: "antigravity", Status: StatusActive, Attributes: map[string]string{AttributePath: path, "excluded_models": "new-model"}}
	snapshot.BindFileSnapshot(path, data)
	if _, errUpdate := manager.Update(WithSkipPersist(ctx), snapshot); errUpdate != nil {
		t.Fatalf("snapshot rejected: %v", errUpdate)
	}
	applied, _ := manager.GetByID(authID)
	if applied.Attributes["excluded_models"] != "new-model" || !applied.Quota.Exceeded {
		t.Fatalf("snapshot did not apply over the cooldown: %+v", applied)
	}

	manager.MarkResult(ctx, Result{AuthID: authID, Provider: "antigravity", Success: true, ExecutionSeq: staleSeq})
	after, _ := manager.GetByID(authID)
	if !after.Quota.Exceeded || !after.NextRetryAfter.After(time.Now()) {
		t.Fatalf("stale success cleared the cooldown after a snapshot: %+v", after)
	}

	manager.MarkResult(ctx, Result{AuthID: authID, Provider: "antigravity", Success: true, ExecutionSeq: manager.nextExecutionSeq()})
	if final, _ := manager.GetByID(authID); final.Quota.Exceeded {
		t.Fatalf("a success dispatched after the cooldown did not clear it: %+v", final)
	}
}

// An update that is no file snapshot keeps an active credential_quota cooldown (the
// carry-over in updateInternalLocked) together with its watermark (#6643).
func TestManager_Issue6643_CredentialQuotaCarryKeepsCooldownWatermark(t *testing.T) {
	const authID = "credential-quota-watermark-auth"
	manager := issue6643SeqTestAuth(t, authID)
	ctx := context.Background()

	retry := 3 * time.Hour
	manager.MarkResult(ctx, Result{
		AuthID: authID, Provider: "antigravity", Model: "m", ExecutionSeq: manager.nextExecutionSeq(),
		Error:      &Error{HTTPStatus: http.StatusTooManyRequests, Code: "usage_limit_reached", Message: "quota exceeded"},
		RetryAfter: &retry, CredentialScope: true,
	})
	cooled, _ := manager.GetByID(authID)
	if !cooled.Quota.Exceeded || cooled.Quota.Reason != "credential_quota" || cooled.cooldownRecordedSeq == 0 {
		t.Fatalf("expected a credential_quota cooldown with a watermark, got quota=%+v seq=%d", cooled.Quota, cooled.cooldownRecordedSeq)
	}

	if _, errUpdate := manager.Update(WithSkipPersist(ctx), &Auth{ID: authID, Provider: "antigravity", Status: StatusActive}); errUpdate != nil {
		t.Fatalf("update: %v", errUpdate)
	}
	got, _ := manager.GetByID(authID)
	if got.Quota.Reason != "credential_quota" || got.cooldownRecordedSeq != cooled.cooldownRecordedSeq {
		t.Fatalf("after the update quota=%+v seq=%d, want the credential_quota cooldown with seq %d", got.Quota, got.cooldownRecordedSeq, cooled.cooldownRecordedSeq)
	}
}

func TestUpdateKeepsNewerIncomingReserveWindow(t *testing.T) {
	now := time.Now()
	older := quotaReserveWindow{ObservedAt: now.Add(-time.Hour), ResetAt: now.Add(24 * time.Hour), UsageTrusted: true, UsedPercent: 10}
	newer := quotaReserveWindow{ObservedAt: now, ResetAt: now.Add(24 * time.Hour), UsageTrusted: true, UsedPercent: 90}
	cases := []struct {
		name     string
		existing QuotaState
		snapshot bool
	}{
		{name: "file snapshot", existing: QuotaState{ReserveWindows: map[string]quotaReserveWindow{"weekly": older}}, snapshot: true},
		{name: "active credential_quota cooldown", existing: QuotaState{
			Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(time.Hour),
			ReserveWindows: map[string]quotaReserveWindow{"weekly": older},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const authID = "reserve-window-update-auth"
			ctx := WithSkipPersist(context.Background())
			manager := NewManager(nil, nil, nil)
			if _, errRegister := manager.Register(ctx, &Auth{ID: authID, Provider: "codex", Status: StatusActive, Quota: tc.existing}); errRegister != nil {
				t.Fatalf("register: %v", errRegister)
			}
			incoming := &Auth{ID: authID, Provider: "codex", Status: StatusActive, Quota: QuotaState{ReserveWindows: map[string]quotaReserveWindow{"weekly": newer}}}
			if tc.snapshot {
				path := filepath.Join(t.TempDir(), authID+".json")
				data := []byte(`{"type":"codex"}`)
				if errWrite := os.WriteFile(path, data, 0o600); errWrite != nil {
					t.Fatal(errWrite)
				}
				incoming.Attributes = map[string]string{AttributePath: path}
				incoming.BindFileSnapshot(path, data)
			}
			if _, errUpdate := manager.Update(ctx, incoming); errUpdate != nil {
				t.Fatalf("update: %v", errUpdate)
			}
			got, _ := manager.GetByID(authID)
			if window := got.Quota.ReserveWindows["weekly"]; !window.ObservedAt.Equal(newer.ObservedAt) || window.UsedPercent != newer.UsedPercent {
				t.Fatalf("weekly reserve window = %+v, want the newer incoming window %+v", window, newer)
			}
		})
	}
}
