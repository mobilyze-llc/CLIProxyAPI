package auth

import (
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func (m *Manager) quotaReserves() []config.QuotaReserve {
	cfg, _ := m.runtimeConfig.Load().(*config.Config)
	if cfg == nil {
		return nil
	}
	return cfg.Routing.QuotaReserves
}

func (m *Manager) quotaReserveReached(auth *Auth, now time.Time) bool {
	kind, email := auth.AccountInfo()
	if kind != "oauth" || email == "" {
		return false
	}
	for _, reserve := range m.quotaReserves() {
		if reserve.Percent <= 0 || !strings.EqualFold(strings.TrimSpace(reserve.Provider), auth.Provider) ||
			!strings.EqualFold(strings.TrimSpace(reserve.Email), email) {
			continue
		}
		if auth.Quota.ObservedAt.IsZero() {
			return true
		}

		windows := auth.Quota.reserveWindows(auth.Provider)
		if len(windows) == 0 || strings.EqualFold(auth.Provider, "claude") && len(windows) < 2 {
			return true
		}
		for _, window := range windows {
			if !window.ResetAt.IsZero() && !window.ResetAt.After(now) {
				continue
			}
			if !window.UsageKnown || window.Exhausted || window.UsedPercent >= 100-reserve.Percent {
				return true
			}
		}
	}
	return false
}
