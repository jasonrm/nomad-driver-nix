package nix

import (
	"net/netip"
	"os"
	"strings"

	"github.com/hashicorp/nomad/plugins/drivers"
)

var fallbackDNSServers = []string{"1.1.1.1", "8.8.8.8"}

func defaultDNSConfig(resolvPath string) (*drivers.DNSConfig, string, error) {
	content, err := os.ReadFile(resolvPath)
	if err != nil {
		return nil, "", err
	}

	nameservers := resolvConfNameservers(string(content))
	switch {
	case len(nameservers) == 0:
		return &drivers.DNSConfig{Servers: cloneStrings(fallbackDNSServers)}, "host resolv.conf has no nameservers", nil
	case allLoopbackNameservers(nameservers):
		return &drivers.DNSConfig{Servers: cloneStrings(fallbackDNSServers)}, "host resolv.conf only contains loopback nameservers", nil
	default:
		return nil, "", nil
	}
}

func resolvConfNameservers(content string) []string {
	var nameservers []string
	for _, line := range strings.Split(content, "\n") {
		line, _, _ = strings.Cut(line, "#")
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "nameserver" {
			nameservers = append(nameservers, fields[1])
		}
	}
	return nameservers
}

func allLoopbackNameservers(nameservers []string) bool {
	for _, nameserver := range nameservers {
		addr, err := netip.ParseAddr(strings.Trim(nameserver, "[]"))
		if err != nil || !addr.IsLoopback() {
			return false
		}
	}
	return len(nameservers) > 0
}

func cloneStrings(values []string) []string {
	cloned := make([]string, len(values))
	copy(cloned, values)
	return cloned
}
