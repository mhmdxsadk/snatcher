// Package config loads Snatcher's process configuration.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/mhmdxsadk/snatcher/internal/security"
)

type Config struct {
	ListenAddr string
	CobaltURL  string
	Security   security.Config
}

func Load(getenv func(string) string) (Config, error) {
	c := Config{
		ListenAddr: strings.TrimSpace(getenv("LISTEN")),
		CobaltURL:  strings.TrimSpace(getenv("COBALT_API")),
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
	if err := ValidateCobaltURL(c.CobaltURL); err != nil {
		return Config{}, err
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

// ValidateCobaltURL accepts an HTTP(S) base URL, including a reverse proxy path.
func ValidateCobaltURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("COBALT_API must be an absolute HTTP(S) URL")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("COBALT_API must not contain credentials, a query, or a fragment")
	}
	if port := u.Port(); port != "" || strings.HasSuffix(u.Host, ":") {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return errors.New("COBALT_API port must be an integer between 1 and 65535")
		}
	}
	return nil
}
