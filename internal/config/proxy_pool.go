package config

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode"
)

// ProxyPoolEntry describes a reusable outbound proxy managed by operators.
type ProxyPoolEntry struct {
	ID          string `yaml:"id" json:"id"`
	Name        string `yaml:"name" json:"name"`
	URL         string `yaml:"url" json:"url"`
	SourceIP    string `yaml:"source-ip,omitempty" json:"source-ip,omitempty"`
	Enabled     bool   `yaml:"enabled" json:"enabled"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// ValidateProxyURL verifies that a proxy URL can be used by the shared transport builders.
func ValidateProxyURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Errorf("proxy url is required")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("invalid proxy url: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("proxy url must include scheme and host")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5":
		return nil
	default:
		return fmt.Errorf("unsupported proxy scheme: %s", parsed.Scheme)
	}
}

// ValidateSourceIP verifies that a source IP can be used for direct egress binding.
func ValidateSourceIP(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Errorf("source ip is required")
	}
	parsed := net.ParseIP(trimmed)
	if parsed == nil {
		return fmt.Errorf("invalid source ip")
	}
	if parsed = parsed.To4(); parsed == nil {
		return fmt.Errorf("source ip must be an IPv4 address")
	}
	return nil
}

// NormalizeProxyPool trims entries, removes invalid rows and keeps the first entry per ID.
func NormalizeProxyPool(entries []ProxyPoolEntry) []ProxyPoolEntry {
	if len(entries) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(entries))
	out := make([]ProxyPoolEntry, 0, len(entries))
	for _, entry := range entries {
		entry.ID = normalizeProxyID(entry.ID)
		entry.Name = strings.TrimSpace(entry.Name)
		entry.URL = strings.TrimSpace(entry.URL)
		entry.SourceIP = strings.TrimSpace(entry.SourceIP)
		entry.Description = strings.TrimSpace(entry.Description)
		if entry.URL == "" && entry.SourceIP == "" {
			continue
		}
		if entry.URL != "" && ValidateProxyURL(entry.URL) != nil {
			continue
		}
		if entry.SourceIP != "" && ValidateSourceIP(entry.SourceIP) != nil {
			continue
		}
		if entry.ID == "" {
			switch {
			case entry.SourceIP != "":
				entry.ID = proxyIDFromSourceIP(entry.SourceIP)
			case entry.URL != "":
				entry.ID = proxyIDFromURL(entry.URL)
			}
		}
		if entry.Name == "" {
			entry.Name = entry.ID
		}
		if _, ok := seen[entry.ID]; ok {
			continue
		}
		seen[entry.ID] = struct{}{}
		out = append(out, entry)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SanitizeProxyPool normalizes the configured reusable proxy list in-place.
func (cfg *Config) SanitizeProxyPool() {
	if cfg == nil {
		return
	}
	cfg.ProxyPool = NormalizeProxyPool(cfg.ProxyPool)
}

// ResolveProxyURL returns the effective proxy URL for a proxy-id plus legacy fallback URL.
func (cfg *Config) ResolveProxyURL(proxyID string, fallbackURL string) string {
	if cfg != nil {
		id := normalizeProxyID(proxyID)
		if id != "" {
			for _, entry := range cfg.ProxyPool {
				if entry.Enabled && normalizeProxyID(entry.ID) == id {
					if strings.TrimSpace(entry.SourceIP) != "" {
						return SourceIPTransportURL(entry.SourceIP)
					}
					if strings.TrimSpace(entry.URL) != "" {
						return strings.TrimSpace(entry.URL)
					}
				}
			}
		}
	}
	if fallback := strings.TrimSpace(fallbackURL); fallback != "" {
		return fallback
	}
	if cfg != nil {
		return strings.TrimSpace(cfg.ProxyURL)
	}
	return ""
}

func normalizeProxyID(raw string) string {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if trimmed == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range trimmed {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func proxyIDFromURL(raw string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(raw)))
	return "proxy-" + hex.EncodeToString(sum[:])[:10]
}

func proxyIDFromSourceIP(raw string) string {
	return "source-ip-" + normalizeProxyID(raw)
}

// SourceIPTransportURL converts a direct source IP binding into the internal transport URL form.
func SourceIPTransportURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if ValidateSourceIP(trimmed) != nil {
		return ""
	}
	return "sourceip://" + trimmed
}
