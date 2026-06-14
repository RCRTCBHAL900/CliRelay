package auth

import (
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	internalusage "github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

func normalizeLoadedClaudeProxyAssignments(cfg *internalconfig.Config, items []*Auth) []*Auth {
	if len(items) == 0 {
		return nil
	}
	pool := listEnabledProxyPoolEntries(cfg)
	if len(pool) == 0 {
		return nil
	}

	assignments := make(map[string]int, len(pool))
	for _, entry := range pool {
		if key := proxyPoolAssignmentKey(entry); key != "" {
			assignments[key] = 0
		}
	}
	for _, auth := range items {
		if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") {
			continue
		}
		if key := assignedAuthProxyKey(auth); key != "" {
			if _, ok := assignments[key]; ok {
				assignments[key]++
			}
		}
	}

	updated := make([]*Auth, 0)
	for _, auth := range items {
		if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") {
			continue
		}
		if strings.TrimSpace(auth.ProxyID) != "" || strings.TrimSpace(auth.ProxyURL) != "" {
			continue
		}
		bestIndex := -1
		bestCount := int(^uint(0) >> 1)
		for idx, entry := range pool {
			count := assignments[proxyPoolAssignmentKey(entry)]
			if bestIndex == -1 || count < bestCount {
				bestIndex = idx
				bestCount = count
			}
		}
		if bestIndex < 0 {
			continue
		}
		entry := pool[bestIndex]
		auth.ProxyID = strings.TrimSpace(entry.ID)
		auth.ProxyURL = resolveProxyEntryURL(entry)
		if auth.Metadata != nil {
			if auth.ProxyID != "" {
				auth.Metadata["proxy_id"] = auth.ProxyID
			}
			if auth.ProxyURL != "" {
				auth.Metadata["proxy_url"] = auth.ProxyURL
			}
		}
		assignments[proxyPoolAssignmentKey(entry)]++
		updated = append(updated, auth)
	}
	return updated
}

func listEnabledProxyPoolEntries(cfg *internalconfig.Config) []internalconfig.ProxyPoolEntry {
	if cfg == nil {
		return nil
	}
	internalusage.ApplyStoredProxyPool(cfg)
	out := make([]internalconfig.ProxyPoolEntry, 0, len(cfg.ProxyPool))
	for _, entry := range cfg.ProxyPool {
		if !entry.Enabled {
			continue
		}
		if strings.TrimSpace(entry.URL) == "" && strings.TrimSpace(entry.SourceIP) == "" {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func proxyPoolAssignmentKey(entry internalconfig.ProxyPoolEntry) string {
	if sourceIP := strings.TrimSpace(entry.SourceIP); sourceIP != "" {
		return normalizeProxyAssignmentID(internalconfig.SourceIPTransportURL(sourceIP))
	}
	if proxyURL := strings.TrimSpace(entry.URL); proxyURL != "" {
		return normalizeProxyAssignmentID(proxyURL)
	}
	return normalizeProxyAssignmentID(entry.ID)
}

func assignedAuthProxyKey(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
		return normalizeProxyAssignmentID(proxyURL)
	}
	if proxyID := strings.TrimSpace(auth.ProxyID); proxyID != "" {
		return normalizeProxyAssignmentID(proxyID)
	}
	return ""
}

func resolveProxyEntryURL(entry internalconfig.ProxyPoolEntry) string {
	if sourceIP := strings.TrimSpace(entry.SourceIP); sourceIP != "" {
		return internalconfig.SourceIPTransportURL(sourceIP)
	}
	return strings.TrimSpace(entry.URL)
}

func normalizeProxyAssignmentID(raw string) string {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if trimmed == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range trimmed {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
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
