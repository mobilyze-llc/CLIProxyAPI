package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestQuotaReserveConfigLoadAndValidation(t *testing.T) {
	for _, version := range []string{"", "config-version: 8\n"} {
		for _, tc := range []struct {
			name, provider, email, percent string
			valid                          bool
		}{
			{"valid", "claude", "personal@example.com", "20", true},
			{"codex", "codex", "personal@example.com", "20", true},
			{"disabled", "codex", "personal@example.com", "0", true},
			{"provider", "other", "personal@example.com", "20", false},
			{"email", "claude", "", "20", false},
			{"negative", "claude", "personal@example.com", "-1", false},
			{"too-large", "claude", "personal@example.com", "101", false},
			{"nan", "claude", "personal@example.com", ".nan", false},
			{"infinity", "claude", "personal@example.com", ".inf", false},
		} {
			t.Run(version+tc.name, func(t *testing.T) {
				raw := []byte(fmt.Sprintf("%srouting:\n  quota-reserves:\n    - provider: %s\n      email: %q\n      percent: %s\n", version, tc.provider, tc.email, tc.percent))
				path := filepath.Join(t.TempDir(), "config.yaml")
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				cfg, err := LoadConfig(path)
				if (err == nil) != tc.valid {
					t.Fatalf("LoadConfig error=%v, valid=%v", err, tc.valid)
				}
				if version != "" {
					if err := ValidateV8Config(raw); (err == nil) != tc.valid {
						t.Fatalf("ValidateV8Config error=%v, valid=%v", err, tc.valid)
					}
				}
				if tc.valid && (len(cfg.Routing.QuotaReserves) != 1 || cfg.Routing.QuotaReserves[0].Email != tc.email) {
					t.Fatalf("lost reserve configuration: %+v", cfg.Routing)
				}
			})
		}
	}
}
