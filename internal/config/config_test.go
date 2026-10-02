package config

import (
	"strings"
	"testing"
)

func TestSecurityConfig(t *testing.T) {
	base := map[string]string{"SNATCHER_API_KEY": strings.Repeat("k", 32)}
	cfg, err := Load(func(k string) string { return base[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Security.IPPerMinute != 20 || cfg.Security.IPBurst != 5 || cfg.Security.GlobalPerMinute != 60 || cfg.Security.MaxConcurrent != 4 {
		t.Fatalf("unexpected defaults: %+v", cfg.Security)
	}
	for _, tt := range []struct{ key, value string }{
		{"JOB_TTL", "0"}, {"JOB_TTL", "30s"}, {"JOB_TTL", "25h"}, {"JOB_TTL", "no"},
		{"COBALT_API", "file:///tmp/test"}, {"COBALT_API", "http://user:password@cobalt:9000"}, {"COBALT_API", "http://cobalt:9000/?key=value"}, {"COBALT_API_KEY", "key\nheader"},
		{"STORAGE_LIMIT_BYTES", "garbage"}, {"STORAGE_LIMIT_BYTES", "0"}, {"STORAGE_LIMIT_BYTES", "1073741823"},
		{"LISTEN", "localhost"}, {"LISTEN", "localhost:"}, {"LISTEN", ":invalid"}, {"LISTEN", ":65536"},
		{"SNATCHER_API_KEY", ""}, {"SNATCHER_API_KEY", "short"}, {"SNATCHER_API_KEY", strings.Repeat(" ", 32)},
		{"RATE_LIMIT_PER_IP", "0"}, {"RATE_BURST_PER_IP", "-1"}, {"RATE_LIMIT_GLOBAL", "no"},
		{"MAX_CONCURRENT", "10001"}, {"MAX_RATE_CLIENTS", "0"},
		{"TRUSTED_PROXIES", "garbage"}, {"TRUSTED_PROXIES", "0.0.0.0/0"},
	} {
		t.Run(tt.key+tt.value, func(t *testing.T) {
			_, err := Load(func(k string) string {
				if k == tt.key {
					return tt.value
				}
				return base[k]
			})
			if err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("expected %s error, got %v", tt.key, err)
			}
		})
	}
	cfg, err = Load(func(k string) string {
		switch k {
		case "TRUSTED_PROXIES":
			return "127.0.0.1/32, ::1/128"
		case "STORAGE_LIMIT_BYTES":
			return "2147483648"
		case "RATE_LIMIT_GLOBAL":
			return "120"
		}
		return base[k]
	})
	if err != nil || len(cfg.Security.TrustedProxies) != 2 || cfg.Security.GlobalPerMinute != 120 || cfg.StorageLimit != 2147483648 {
		t.Fatal("overrides failed", err)
	}
}
