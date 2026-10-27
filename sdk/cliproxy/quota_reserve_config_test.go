package cliproxy

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"math"
	"testing"
)

func TestQuotaReserveConfigBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		percent float64
		home    bool
	}{
		{"too-large", 101, false}, {"negative", -1, false}, {"nan", math.NaN(), false}, {"home", 20, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{Home: config.HomeConfig{Enabled: tc.home}, Routing: config.RoutingConfig{QuotaReserves: []config.QuotaReserve{{Provider: "claude", Email: "personal@example.com", Percent: tc.percent}}}}
			if _, err := NewBuilder().WithConfig(cfg).WithConfigPath("unused.yaml").Build(); err == nil {
				t.Fatal("SDK builder accepted invalid reserve")
			}
			original := &config.Config{}
			service := &Service{cfg: original}
			if commit := service.commitConfigUpdate(cfg); commit.cfg != nil {
				t.Fatal("reload accepted invalid reserve")
			}
			if service.cfg != original {
				t.Fatal("rejected reload replaced configuration")
			}
		})
	}
}
