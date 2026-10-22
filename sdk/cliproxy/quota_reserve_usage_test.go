package cliproxy

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestQuotaReservePartialClaudeUsageDoesNotRestoreEligibility(t *testing.T) {
	reset := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	low := fmt.Sprintf(`{"utilization":10,"resets_at":%q}`, reset)
	high := fmt.Sprintf(`{"utilization":95,"resets_at":%q}`, reset)
	for _, limiting := range []string{"five_hour", "seven_day"} {
		for _, partial := range []struct{ name, window string }{
			{"null-usage", fmt.Sprintf(`{"utilization":null,"resets_at":%q}`, reset)},
			{"missing-usage", fmt.Sprintf(`{"resets_at":%q}`, reset)},
			{"missing-reset", `{"utilization":95}`},
			{"unusable", `{"utilization":null,"resets_at":null}`},
			{"unknown-without-reset", `{"utilization":null}`},
			{"empty-object", `{}`},
			{"invalid-type", `"unusable"`},
			{"null-window", `null`},
			{"omitted-window", ""},
		} {
			t.Run(limiting+"/"+partial.name, func(t *testing.T) {
				cfg := &config.Config{Routing: config.RoutingConfig{QuotaReserves: []config.QuotaReserve{{Provider: "claude", Email: "personal@example.com", Percent: 20}}}}
				manager := coreauth.NewManager(nil, nil, nil)
				manager.SetConfig(cfg)
				manager.RegisterExecutor(executor.NewClaudeExecutor(cfg))
				ctx := coreauth.WithSkipPersist(context.Background())
				if _, errRegister := manager.Register(ctx, &coreauth.Auth{ID: "protected", Provider: "claude", Status: coreauth.StatusActive, Metadata: map[string]any{"email": "personal@example.com", "access_token": "synthetic-token"}}); errRegister != nil {
					t.Fatal(errRegister)
				}
				windows := map[string]string{"five_hour": low, "seven_day": low}
				observe := func(window string) {
					t.Helper()
					windows[limiting] = window
					body := []byte(fmt.Sprintf(`{"five_hour":%s,"seven_day":%s}`, windows["five_hour"], windows["seven_day"]))
					if window == "" {
						other := "five_hour"
						if limiting == other {
							other = "seven_day"
						}
						body = []byte(fmt.Sprintf(`{%q:%s}`, other, low))
					}
					if !manager.ObserveQuotaHeaders("protected", helps.ParseClaudeUsageHeaders(body)) {
						t.Fatal("usage response was not observed")
					}
					if window == partial.window {
						observed, _ := manager.GetByID("protected")
						prefix := "Anthropic-Ratelimit-Unified-5h-"
						if limiting == "seven_day" {
							prefix = "Anthropic-Ratelimit-Unified-7d-"
						}
						for key := range observed.Quota.Signals {
							if strings.HasPrefix(key, prefix) {
								t.Fatalf("incomplete usage window entered ordinary snapshot: %s", key)
							}
						}
					}
				}
				observe(partial.window)
				if _, errSelect := manager.SelectAuth(ctx, "claude", "", coreexecutor.Options{}); errSelect == nil {
					t.Fatal("first incomplete reading admitted the protected account")
				}
				if partial.name == "missing-reset" {
					observe(low)
					observe(partial.window)
					if _, errSelect := manager.SelectAuth(ctx, "claude", "", coreexecutor.Options{}); errSelect == nil {
						t.Fatal("known high usage without reset was lost after a prior low reading")
					}
				}
				observe(high)
				if _, errSelect := manager.SelectAuth(ctx, "claude", "", coreexecutor.Options{}); errSelect == nil {
					t.Fatal("high usage must exclude the account")
				}
				observe(partial.window)
				if _, errSelect := manager.SelectAuth(ctx, "claude", "", coreexecutor.Options{}); errSelect == nil {
					t.Fatal("partial usage response reopened the protected account")
				}
				observe(low)
				if selected, errSelect := manager.SelectAuth(ctx, "claude", "", coreexecutor.Options{}); errSelect != nil || selected == nil || selected.ID != "protected" {
					t.Fatalf("below-threshold response did not restore eligibility: %v", errSelect)
				}
			})
		}
	}
}

func TestQuotaReserveCodexWeeklyOnlyRemainsEligible(t *testing.T) {
	cfg := &config.Config{Routing: config.RoutingConfig{QuotaReserves: []config.QuotaReserve{{Provider: "codex", Email: "personal@example.com", Percent: 20}}}}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(executor.NewCodexExecutor(cfg))
	ctx := coreauth.WithSkipPersist(context.Background())
	if _, errRegister := manager.Register(ctx, &coreauth.Auth{ID: "protected", Provider: "codex", Status: coreauth.StatusActive, Metadata: map[string]any{"email": "personal@example.com", "access_token": "synthetic-token"}}); errRegister != nil {
		t.Fatal(errRegister)
	}
	body := []byte(fmt.Sprintf(`{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":604800,"reset_at":%d},"secondary_window":null}}`, time.Now().Add(time.Hour).Unix()))
	if !manager.ObserveQuotaHeaders("protected", helps.ParseCodexUsageHeaders(body)) {
		t.Fatal("weekly-only Codex usage was not observed")
	}
	if selected, errSelect := manager.SelectAuth(ctx, "codex", "", coreexecutor.Options{}); errSelect != nil || selected == nil || selected.ID != "protected" {
		t.Fatalf("weekly-only Codex account was excluded: %v", errSelect)
	}
}
