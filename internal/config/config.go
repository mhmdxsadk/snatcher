// Package config loads Snatcher's process configuration.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/mhmdxsadk/snatcher/internal/security"
)

type Config struct {
	ListenAddr  string
	DownloadDir string
	Security    security.Config
}

func Load(getenv func(string) string) (Config, error) {
	c := Config{
		ListenAddr: strings.TrimSpace(getenv("LISTEN")),
	}
	if c.ListenAddr == "" {
		c.ListenAddr = "127.0.0.1:8080"
	}
	_, port, err := net.SplitHostPort(c.ListenAddr)
	if err != nil {
		return Config{}, errors.New("LISTEN must be a host:port address")
	}
	// Port zero is valid for an automatically assigned listener port.
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return Config{}, errors.New("LISTEN port must be an integer between 0 and 65535")
	}
	c.DownloadDir = strings.TrimSpace(getenv("DOWNLOAD_DIR"))
	if c.DownloadDir == "" {
		c.DownloadDir = "/tmp/snatcher-downloads"
	}
	c.Security.APIKey = getenv("SNATCHER_API_KEY")
	if len(c.Security.APIKey) < 32 || len(c.Security.APIKey) > 512 || strings.IndexFunc(c.Security.APIKey, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return Config{}, errors.New("SNATCHER_API_KEY must contain 32 to 512 printable ASCII characters without spaces")
	}
	for _, setting := range []struct {
		name              string
		target            *int
		fallback, maximum int
	}{
		{"RATE_LIMIT_PER_IP", &c.Security.IPPerMinute, 20, 1000000},
		{"RATE_BURST_PER_IP", &c.Security.IPBurst, 5, 1000000},
		{"RATE_LIMIT_GLOBAL", &c.Security.GlobalPerMinute, 60, 1000000},
		{"RATE_BURST_GLOBAL", &c.Security.GlobalBurst, 10, 1000000},
		{"MAX_CONCURRENT", &c.Security.MaxConcurrent, 4, 10000},
		{"MAX_RATE_CLIENTS", &c.Security.MaxClients, 10000, 1000000},
	} {
		raw := strings.TrimSpace(getenv(setting.name))
		value := setting.fallback
		if raw != "" {
			var err error
			value, err = strconv.Atoi(raw)
			if err != nil || value < 1 || value > setting.maximum {
				return Config{}, fmt.Errorf("%s must be an integer between 1 and %d", setting.name, setting.maximum)
			}
		}
		*setting.target = value
	}
	if raw := strings.TrimSpace(getenv("TRUSTED_PROXIES")); raw != "" {
		for _, entry := range strings.Split(raw, ",") {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(entry))
			if err != nil || prefix.Bits() == 0 {
				return Config{}, errors.New("TRUSTED_PROXIES must contain comma-separated proxy CIDRs, excluding /0")
			}
			c.Security.TrustedProxies = append(c.Security.TrustedProxies, prefix.Masked())
		}
	}
	return c, nil
}
