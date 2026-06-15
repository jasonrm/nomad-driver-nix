package nix

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDefaultDNSConfigUsesFallbackForLoopbackNameservers(t *testing.T) {
	path := writeResolvConf(t, `
nameserver 127.0.0.53
nameserver ::1
options edns0 trust-ad
`)

	cfg, reason, err := defaultDNSConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		t.Fatal("expected fallback DNS config")
	}
	if reason == "" {
		t.Fatal("expected fallback reason")
	}
	if !reflect.DeepEqual(cfg.Servers, fallbackDNSServers) {
		t.Fatalf("unexpected servers: got %#v want %#v", cfg.Servers, fallbackDNSServers)
	}
}

func TestDefaultDNSConfigUsesFallbackForMissingNameservers(t *testing.T) {
	path := writeResolvConf(t, "options edns0 trust-ad\n")

	cfg, reason, err := defaultDNSConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		t.Fatal("expected fallback DNS config")
	}
	if reason == "" {
		t.Fatal("expected fallback reason")
	}
	if !reflect.DeepEqual(cfg.Servers, fallbackDNSServers) {
		t.Fatalf("unexpected servers: got %#v want %#v", cfg.Servers, fallbackDNSServers)
	}
}

func TestDefaultDNSConfigKeepsUsableNameservers(t *testing.T) {
	path := writeResolvConf(t, `
nameserver 10.0.0.2
nameserver 1.1.1.1
`)

	cfg, reason, err := defaultDNSConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		t.Fatalf("expected no fallback DNS config, got %#v", cfg)
	}
	if reason != "" {
		t.Fatalf("expected no fallback reason, got %q", reason)
	}
}

func writeResolvConf(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "resolv.conf")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}
