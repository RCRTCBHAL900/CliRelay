package auth

import (
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	internalusage "github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

func assignAutomaticClaudeProxy(cfg *internalconfig.Config, auth *Auth, existing []*Auth) bool {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") {
		return false
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}

	pool := listEnabledProxyPoolEntries(cfg)
	changed := false
	if len(pool) > 0 {
		if matched := matchProxyPoolEntryForAuth(pool, auth); matched != nil {
			if applyProxyEntryToAuth(auth, *matched) {
				changed = true
			}
		} else if strings.TrimSpace(auth.ProxyID) == "" && strings.TrimSpace(auth.ProxyURL) == "" {
			if assigned := selectAutomaticClaudeProxyEntry(pool, auth.ID, existing); assigned != nil {
				if applyProxyEntryToAuth(auth, *assigned) {
					changed = true
				}
			}
		}
	}

	if syncClaudeProxyMetadata(auth) {
		changed = true
	}
	return changed
}

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

func selectAutomaticClaudeProxyEntry(pool []internalconfig.ProxyPoolEntry, authID string, existing []*Auth) *internalconfig.ProxyPoolEntry {
	if len(pool) == 0 {
		return nil
	}
	assignments := make(map[string]int, len(pool))
	for _, entry := range pool {
		if key := proxyPoolAssignmentKey(entry); key != "" {
			assignments[key] = 0
		}
	}
	for _, existingAuth := range existing {
		if existingAuth == nil || existingAuth.ID == authID {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(existingAuth.Provider), "claude") {
			continue
		}
		if key := assignedAuthProxyKey(existingAuth); key != "" {
			if _, ok := assignments[key]; ok {
				assignments[key]++
			}
		}
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
		return nil
	}
	entry := pool[bestIndex]
	return &entry
}

func matchProxyPoolEntryForAuth(pool []internalconfig.ProxyPoolEntry, auth *Auth) *internalconfig.ProxyPoolEntry {
	if auth == nil || len(pool) == 0 {
		return nil
	}
	authKey := assignedAuthProxyKey(auth)
	if authKey == "" {
		return nil
	}
	for idx, entry := range pool {
		if proxyPoolAssignmentKey(entry) == authKey {
			return &pool[idx]
		}
	}
	return nil
}

func applyProxyEntryToAuth(auth *Auth, entry internalconfig.ProxyPoolEntry) bool {
	if auth == nil {
		return false
	}
	changed := false
	if proxyID := strings.TrimSpace(entry.ID); proxyID != "" && strings.TrimSpace(auth.ProxyID) != proxyID {
		auth.ProxyID = proxyID
		changed = true
	}
	if proxyURL := resolveProxyEntryURL(entry); proxyURL != "" && strings.TrimSpace(auth.ProxyURL) != proxyURL {
		auth.ProxyURL = proxyURL
		changed = true
	}
	return changed
}

func syncClaudeProxyMetadata(auth *Auth) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	changed := false
	if proxyID := strings.TrimSpace(auth.ProxyID); proxyID != "" {
		if current, _ := auth.Metadata["proxy_id"].(string); strings.TrimSpace(current) != proxyID {
			auth.Metadata["proxy_id"] = proxyID
			changed = true
		}
	}
	if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
		if current, _ := auth.Metadata["proxy_url"].(string); strings.TrimSpace(current) != proxyURL {
			auth.Metadata["proxy_url"] = proxyURL
			changed = true
		}
	}
	return changed
}
