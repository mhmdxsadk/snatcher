// Package config loads Snatcher's process configuration.
package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

type Config struct {
	ListenAddr string
	CobaltURL  string
}

func Load(getenv func(string) string) (Config, error) {
	c := Config{
		ListenAddr: strings.TrimSpace(getenv("LISTEN")),
		CobaltURL:  strings.TrimSpace(getenv("COBALT")),
	}
	if c.ListenAddr == "" {
		c.ListenAddr = "127.0.0.1:8080"
	}
	if _, _, err := net.SplitHostPort(c.ListenAddr); err != nil {
		return Config{}, fmt.Errorf("LISTEN must be a host:port address")
	}
	if err := ValidateCobaltURL(c.CobaltURL); err != nil {
		return Config{}, err
	}
	return c, nil
}

// ValidateCobaltURL accepts an HTTP(S) base URL, including a reverse proxy path.
func ValidateCobaltURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("COBALT must be an absolute HTTP(S) URL")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Errorf("COBALT must not contain credentials, a query, or a fragment")
	}
	return nil
}
