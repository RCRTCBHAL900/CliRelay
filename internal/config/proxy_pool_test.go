package config

import "testing"

func TestNormalizeProxyPoolTrimsDeduplicatesAndValidatesEntries(t *testing.T) {
	t.Parallel()

	input := []ProxyPoolEntry{
		{ID: "  hk-1  ", Name: "  HK 1  ", URL: " socks5://user:pass@127.0.0.1:1080 ", Enabled: true, Description: "  primary  "},
		{ID: "hk-1", Name: "duplicate", URL: "http://127.0.0.1:7890", Enabled: true},
		{ID: "bad", Name: "bad", URL: "ftp://127.0.0.1:21", Enabled: true},
		{ID: "", Name: "auto id", URL: "https://proxy.example.com:8443", Enabled: true},
		{ID: "", Name: "direct", SourceIP: " 152.89.86.108 ", Enabled: true},
		{ID: "claude-lane-01", Name: "Claude Lane 01", Enabled: true},
	}

	got := NormalizeProxyPool(input)

	if len(got) != 4 {
		t.Fatalf("NormalizeProxyPool length = %d, want 4: %#v", len(got), got)
	}
	if got[0].ID != "hk-1" || got[0].Name != "HK 1" || got[0].URL != "socks5://user:pass@127.0.0.1:1080" || got[0].Description != "primary" {
		t.Fatalf("first normalized entry = %#v", got[0])
	}
	if got[1].ID == "" || got[1].URL != "https://proxy.example.com:8443" {
		t.Fatalf("second normalized entry = %#v", got[1])
	}
	if got[2].ID != "source-ip-152-89-86-108" || got[2].SourceIP != "152.89.86.108" {
		t.Fatalf("third normalized entry = %#v", got[2])
	}
	if got[3].ID != "claude-lane-01" || got[3].SourceIP != "" || got[3].URL != "" {
		t.Fatalf("fourth normalized entry = %#v", got[3])
	}
}

func TestValidateProxyURLAllowsSupportedSchemesOnly(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"http://127.0.0.1:7890",
		"https://proxy.example.com:8443",
		"socks5://user:pass@127.0.0.1:1080",
		"sourceip://152.89.86.108",
	} {
		if err := ValidateProxyURL(raw); err != nil {
			t.Fatalf("ValidateProxyURL(%q) returned error: %v", raw, err)
		}
	}

	for _, raw := range []string{"", "127.0.0.1:7890", "ftp://proxy.example.com", "http:///missing-host"} {
		if err := ValidateProxyURL(raw); err == nil {
			t.Fatalf("ValidateProxyURL(%q) returned nil, want error", raw)
		}
	}
}

func TestValidateSourceIPAcceptsIPv4Only(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"152.89.86.108", "127.0.0.1"} {
		if err := ValidateSourceIP(raw); err != nil {
			t.Fatalf("ValidateSourceIP(%q) returned error: %v", raw, err)
		}
	}

	for _, raw := range []string{"", "2001:db8::1", "not-an-ip"} {
		if err := ValidateSourceIP(raw); err == nil {
			t.Fatalf("ValidateSourceIP(%q) returned nil, want error", raw)
		}
	}
}

func TestResolveProxyURLUsesProxyIDBeforeFallback(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		SDKConfig: SDKConfig{ProxyURL: "http://global.example:7890"},
		ProxyPool: []ProxyPoolEntry{
			{ID: "hk", Name: "HK", URL: "socks5://127.0.0.1:1080", Enabled: true},
			{ID: "direct", Name: "Direct", SourceIP: "152.89.86.108", Enabled: true},
			{ID: "disabled", Name: "Disabled", URL: "http://disabled.example:7890", Enabled: false},
		},
	}

	tests := []struct {
		name        string
		proxyID     string
		fallbackURL string
		want        string
	}{
		{name: "proxy id wins", proxyID: "hk", fallbackURL: "http://fallback.example:7890", want: "socks5://127.0.0.1:1080"},
		{name: "source ip route wins", proxyID: "direct", fallbackURL: "http://fallback.example:7890", want: "sourceip://152.89.86.108"},
		{name: "disabled falls back to entry url", proxyID: "disabled", fallbackURL: "http://fallback.example:7890", want: "http://fallback.example:7890"},
		{name: "missing falls back to entry url", proxyID: "missing", fallbackURL: "http://fallback.example:7890", want: "http://fallback.example:7890"},
		{name: "global fallback", proxyID: "", fallbackURL: "", want: "http://global.example:7890"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cfg.ResolveProxyURL(tt.proxyID, tt.fallbackURL); got != tt.want {
				t.Fatalf("ResolveProxyURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRealizeProxyPoolEntriesAutoBindsBlankClaudeLanes(t *testing.T) {
	previousDiscoverer := discoverAutomaticSourceIPs
	discoverAutomaticSourceIPs = func() []string {
		return []string{
			"10.98.0.5",
			"204.168.157.78",
			"65.109.249.42",
			"65.109.249.3",
			"65.109.249.42",
		}
	}
	t.Cleanup(func() {
		discoverAutomaticSourceIPs = previousDiscoverer
	})

	entries := []ProxyPoolEntry{
		{ID: "claude-lane-02", Name: "Claude Lane 02", Enabled: true},
		{ID: "claude-lane-01", Name: "Claude Lane 01", Enabled: true},
		{ID: "explicit", Name: "Explicit", SourceIP: "65.109.249.3", Enabled: true},
	}

	got := RealizeProxyPoolEntries(entries)

	if got[0].SourceIP != "204.168.157.78" {
		t.Fatalf("lane 02 source ip = %q, want %q", got[0].SourceIP, "204.168.157.78")
	}
	if got[1].SourceIP != "65.109.249.42" {
		t.Fatalf("lane 01 source ip = %q, want %q", got[1].SourceIP, "65.109.249.42")
	}
	if got[2].SourceIP != "65.109.249.3" {
		t.Fatalf("explicit source ip = %q, want %q", got[2].SourceIP, "65.109.249.3")
	}
}

func TestResolveProxyURLAutoRealizesClaudeLaneEntries(t *testing.T) {
	previousDiscoverer := discoverAutomaticSourceIPs
	discoverAutomaticSourceIPs = func() []string {
		return []string{"65.109.249.42"}
	}
	t.Cleanup(func() {
		discoverAutomaticSourceIPs = previousDiscoverer
	})

	cfg := &Config{
		SDKConfig: SDKConfig{ProxyURL: "http://global.example:7890"},
		ProxyPool: []ProxyPoolEntry{
			{ID: "claude-lane-01", Name: "Claude Lane 01", Enabled: true},
		},
	}

	if got := cfg.ResolveProxyURL("claude-lane-01", ""); got != "sourceip://65.109.249.42" {
		t.Fatalf("ResolveProxyURL() = %q, want %q", got, "sourceip://65.109.249.42")
	}
}
