// Package netutil provides shared network utilities, including SSRF protection
// helpers used by both web_fetch and install_source.
package netutil

import "net"

// CGNATRange is RFC 6598 shared address space (100.64.0.0/10). Go's IsPrivate
// doesn't cover it, yet some clouds host instance metadata there (Alibaba Cloud
// at 100.100.100.200), so it's an SSRF target to refuse too.
var CGNATRange = mustCIDR("100.64.0.0/10")

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

// BlockedIP reports whether ip is in a private, link-local, CGNAT, or
// unspecified range — all addresses an SSRF guard must not reach.
// Loopback is NOT blocked; callers that need to block it should check
// ip.IsLoopback() separately.
func BlockedIP(ip net.IP) bool {
	return ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() ||
		CGNATRange.Contains(ip)
}
