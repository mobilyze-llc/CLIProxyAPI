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

func (cfg *Config) ValidateQuotaReserves() error {
	if err := cfg.Routing.ValidateQuotaReserves(); err != nil {
		return err
	}
	if cfg.Home.Enabled {
		for _, reserve := range cfg.Routing.QuotaReserves {
			if reserve.Percent > 0 {
				return fmt.Errorf("routing.quota-reserves requires local credential selection; home.enabled must be false")
			}
		}
	}
	return nil
}
