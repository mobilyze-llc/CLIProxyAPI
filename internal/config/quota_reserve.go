package config

import (
	"fmt"
	"math"
	"strings"
)

func (cfg RoutingConfig) ValidateQuotaReserves() error {
	for i, reserve := range cfg.QuotaReserves {
		provider := strings.ToLower(strings.TrimSpace(reserve.Provider))
		if provider != "claude" && provider != "codex" {
			return fmt.Errorf("routing.quota-reserves[%d].provider must be claude or codex", i)
		}
		if strings.TrimSpace(reserve.Email) == "" {
			return fmt.Errorf("routing.quota-reserves[%d].email must not be empty", i)
		}
		if math.IsNaN(reserve.Percent) || math.IsInf(reserve.Percent, 0) || reserve.Percent < 0 || reserve.Percent > 100 {
			return fmt.Errorf("routing.quota-reserves[%d].percent must be between 0 and 100", i)
		}
	}
	return nil
}
