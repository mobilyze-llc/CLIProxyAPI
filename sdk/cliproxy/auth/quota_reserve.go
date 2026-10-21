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

		observed := false
		for _, window := range observedQuotaWindows(auth) {
			if !window.observed {
				continue
			}
			observed = true
			if !window.resetAt.IsZero() && !window.resetAt.After(now) {
				continue
			}
			if !window.usageKnown || window.exhausted || window.usedPercent >= 100-reserve.Percent {
				return true
			}
		}
		if !observed {
			return true
		}
	}
	return false
}
