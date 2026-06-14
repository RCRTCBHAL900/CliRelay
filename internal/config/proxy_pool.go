package config

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"sort"
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

var discoverAutomaticSourceIPs = discoverHostPublicIPv4s

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
	case "sourceip":
		return ValidateSourceIP(parsed.Host)
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
			if !isAutomaticSourceIPLaneEntry(entry) {
				continue
			}
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

// RealizeProxyPoolEntries expands operator-managed automatic source-IP lanes into concrete source IPs.
func RealizeProxyPoolEntries(entries []ProxyPoolEntry) []ProxyPoolEntry {
	if len(entries) == 0 {
		return nil
	}

	out := make([]ProxyPoolEntry, len(entries))
	copy(out, entries)

	automaticIndexes := make([]int, 0, len(out))
	usedSourceIPs := make(map[string]struct{}, len(out))
	for idx, entry := range out {
		if sourceIP := strings.TrimSpace(entry.SourceIP); sourceIP != "" {
			usedSourceIPs[sourceIP] = struct{}{}
		}
		if isAutomaticSourceIPLaneEntry(entry) {
			automaticIndexes = append(automaticIndexes, idx)
		}
	}
	if len(automaticIndexes) == 0 {
		return out
	}

	candidates := make([]string, 0)
	seenCandidates := make(map[string]struct{})
	for _, sourceIP := range discoverAutomaticSourceIPs() {
		trimmed := strings.TrimSpace(sourceIP)
		if ValidateSourceIP(trimmed) != nil || !isPublicIPv4(trimmed) {
			continue
		}
		if _, exists := usedSourceIPs[trimmed]; exists {
			continue
		}
		if _, exists := seenCandidates[trimmed]; exists {
			continue
		}
		seenCandidates[trimmed] = struct{}{}
		candidates = append(candidates, trimmed)
	}
	if len(candidates) == 0 {
		return out
	}

	sort.Slice(candidates, func(i, j int) bool {
		return compareIPv4Strings(candidates[i], candidates[j]) < 0
	})
	sort.Slice(automaticIndexes, func(i, j int) bool {
		left := normalizeProxyID(out[automaticIndexes[i]].ID)
		right := normalizeProxyID(out[automaticIndexes[j]].ID)
		if left != right {
			return left < right
		}
		return automaticIndexes[i] < automaticIndexes[j]
	})

	for idx, entryIndex := range automaticIndexes {
		if idx >= len(candidates) {
			break
		}
		out[entryIndex].SourceIP = candidates[idx]
	}

	return out
}

// SelectAutomaticSourceIPLane returns a deterministic automatic Claude lane for the given seed.
func SelectAutomaticSourceIPLane(entries []ProxyPoolEntry, seed string) *ProxyPoolEntry {
	realizedEntries := RealizeProxyPoolEntries(entries)
	lanes := make([]ProxyPoolEntry, 0, len(realizedEntries))
	for _, entry := range realizedEntries {
		if !entry.Enabled || strings.TrimSpace(entry.SourceIP) == "" {
			continue
		}
		if !strings.HasPrefix(normalizeProxyID(entry.ID), "claude-lane-") {
			continue
		}
		lanes = append(lanes, entry)
	}
	if len(lanes) == 0 {
		return nil
	}
	sort.Slice(lanes, func(i, j int) bool {
		left := normalizeProxyID(lanes[i].ID)
		right := normalizeProxyID(lanes[j].ID)
		if left != right {
			return left < right
		}
		return lanes[i].Name < lanes[j].Name
	})
	trimmedSeed := strings.TrimSpace(seed)
	if trimmedSeed == "" {
		trimmedSeed = "default"
	}
	sum := sha256.Sum256([]byte(trimmedSeed))
	index := int(binary.BigEndian.Uint64(sum[:8]) % uint64(len(lanes)))
	entry := lanes[index]
	return &entry
}

// ResolveProxyURLFromEntries returns the effective proxy URL for a proxy-id using the provided entries.
func ResolveProxyURLFromEntries(entries []ProxyPoolEntry, proxyID string, fallbackURL string, defaultURL string) string {
	realizedEntries := RealizeProxyPoolEntries(entries)
	id := normalizeProxyID(proxyID)
	if id != "" {
		for _, entry := range realizedEntries {
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
	if fallback := strings.TrimSpace(fallbackURL); fallback != "" {
		return fallback
	}
	return strings.TrimSpace(defaultURL)
}

// ResolveProxyURL returns the effective proxy URL for a proxy-id plus legacy fallback URL.
func (cfg *Config) ResolveProxyURL(proxyID string, fallbackURL string) string {
	if cfg == nil {
		return strings.TrimSpace(fallbackURL)
	}
	return ResolveProxyURLFromEntries(cfg.ProxyPool, proxyID, fallbackURL, cfg.ProxyURL)
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

func isAutomaticSourceIPLaneEntry(entry ProxyPoolEntry) bool {
	if strings.TrimSpace(entry.URL) != "" || strings.TrimSpace(entry.SourceIP) != "" {
		return false
	}
	id := normalizeProxyID(entry.ID)
	if id == "" {
		id = normalizeProxyID(entry.Name)
	}
	return strings.HasPrefix(id, "claude-lane-")
}

func discoverHostPublicIPv4s() []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	out := make([]string, 0)
	seen := make(map[string]struct{})
	for _, iface := range interfaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch typed := addr.(type) {
			case *net.IPNet:
				ip = typed.IP
			case *net.IPAddr:
				ip = typed.IP
			default:
				continue
			}
			ip = ip.To4()
			if ip == nil {
				continue
			}
			raw := ip.String()
			if !isPublicIPv4(raw) {
				continue
			}
			if _, exists := seen[raw]; exists {
				continue
			}
			seen[raw] = struct{}{}
			out = append(out, raw)
		}
	}
	return out
}

func isPublicIPv4(raw string) bool {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil {
		return false
	}
	ip = ip.To4()
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsPrivate() || ip.IsMulticast() {
		return false
	}
	// Exclude CGNAT 100.64.0.0/10 and link-local 169.254.0.0/16.
	if ip[0] == 100 && ip[1] >= 64 && ip[1] <= 127 {
		return false
	}
	if ip[0] == 169 && ip[1] == 254 {
		return false
	}
	return true
}

func compareIPv4Strings(left string, right string) int {
	leftIP := net.ParseIP(strings.TrimSpace(left)).To4()
	rightIP := net.ParseIP(strings.TrimSpace(right)).To4()
	if leftIP == nil || rightIP == nil {
		return strings.Compare(left, right)
	}
	return bytes.Compare(leftIP, rightIP)
}
