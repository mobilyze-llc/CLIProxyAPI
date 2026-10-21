package cliproxy

import (
	"context"
	"fmt"
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
					if !manager.ObserveQuotaHeaders("protected", helps.ParseClaudeUsageHeaders(body)) {
						t.Fatal("usage response was not observed")
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
