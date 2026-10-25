// Package netpolicy constrains every local TCP connection to the chosen IP family.
// With a proxy this constrains the connection to the proxy, not its upstream socket.
package netpolicy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

func Validate(family string) error {
	switch family {
	case "", "auto", "ipv4", "ipv6":
		return nil
	default:
		return fmt.Errorf("unsupported IP family %q (want auto, ipv4 or ipv6)", family)
	}
}

// Apply configures an unused, mount-local transport. TCP4/TCP6 deliberately
// disable cross-family fallback, including redirects and SOCKS proxy dialing.
func Apply(t *http.Transport, family string) *http.Transport {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		switch family {
		case "ipv4":
			network = "tcp4"
		case "ipv6":
			network = "tcp6"
		}
		return dialer.DialContext(ctx, network, address)
	}
	return t
}
