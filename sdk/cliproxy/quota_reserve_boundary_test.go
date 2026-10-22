package cliproxy

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestQuotaUsageHollowClaudeReadPreservesOrdinaryRouting(t *testing.T) {
	now := time.Now()
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{})
	manager.SetSelector(&coreauth.SoonestResetSelector{})
	service := &Service{cfg: &config.Config{}, coreManager: manager}
	rt := quotaTestRoundTripper(func(req *http.Request) (*http.Response, error) {
		return quotaTestResponse(req, http.StatusOK, "application/json", `{"five_hour":{"utilization":0,"resets_at":null},"seven_day":{"utilization":0,"resets_at":null}}`), nil
	})
	ctx := coreauth.WithSkipPersist(context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(rt)))
	for _, id := range []string{"exhausted", "healthy"} {
		used, reset := "1", now.Add(time.Hour)
		if id == "healthy" {
			used, reset = "0.1", now.Add(2*time.Hour)
		}
		auth := &coreauth.Auth{ID: id, Provider: "claude", Status: coreauth.StatusActive,
			Metadata: map[string]any{"type": "claude", "access_token": "synthetic-token", "expired": now.Add(240 * time.Hour).Format(time.RFC3339)},
			Quota: coreauth.QuotaState{ObservedAt: now, Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-5h-Utilization": used,
				"Anthropic-Ratelimit-Unified-5h-Reset":       strconv.FormatInt(reset.Unix(), 10),
				"Anthropic-Ratelimit-Unified-7d-Utilization": "0.1",
				"Anthropic-Ratelimit-Unified-7d-Reset":       strconv.FormatInt(reset.Unix(), 10),
			}}}
		service.ensureExecutorsForAuth(auth)
		if _, err := manager.Register(ctx, auth); err != nil {
			t.Fatal(err)
		}
	}
	for _, stage := range []string{"before", "after"} {
		if stage == "after" {
			service.readQuotaUsage(ctx, "exhausted")
		}
		selected, err := manager.SelectAuth(ctx, "claude", "", coreexecutor.Options{})
		if err != nil || selected == nil || selected.ID != "healthy" {
			selectedID := "<nil>"
			if selected != nil {
				selectedID = selected.ID
			}
			t.Fatalf("%s hollow usage read selected %s: error=%v", stage, selectedID, err)
		}
	}
}
