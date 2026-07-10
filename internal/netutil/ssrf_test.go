package netutil

import (
	"net"
	"testing"
)

func TestBlockedIP(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
	}{
		{"10.0.0.1", true},
		{"192.168.1.1", true},
		{"172.16.0.1", true},
		{"100.64.1.1", true}, // CGNAT
		{"169.254.1.1", true},
		{"0.0.0.0", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"127.0.0.1", false}, // loopback not blocked by design
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("parse %s", c.ip)
		}
		got := BlockedIP(ip)
		if got != c.blocked {
			t.Errorf("BlockedIP(%s) = %v, want %v", c.ip, got, c.blocked)
		}
	}
}
