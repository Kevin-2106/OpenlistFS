package netpolicy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func localServer(t *testing.T, network, address string) *httptest.Server {
	t.Helper()
	l, err := net.Listen(network, address)
	if err != nil {
		t.Skipf("host does not support %s loopback: %v", network, err)
	}
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "connected") }))
	s.Listener = l
	s.Start()
	t.Cleanup(s.Close)
	return s
}

func get(t *testing.T, family, address string, proxy func(*http.Request) (*url.URL, error), wantOK bool) {
	t.Helper()
	transport := Apply(&http.Transport{Proxy: proxy}, family)
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	resp, err := client.Get(address)
	if err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	if (err == nil) != wantOK {
		t.Fatalf("family %q, URL %s: err=%v, want success=%v", family, address, err, wantOK)
	}
}

func TestFamilyReallyConstrainsHTTP(t *testing.T) {
	v4 := localServer(t, "tcp4", "127.0.0.1:0")
	for _, family := range []string{"", "auto", "ipv4", "ipv6"} {
		t.Run("IPv4Server/"+family, func(t *testing.T) { get(t, family, v4.URL, nil, family != "ipv6") })
	}
	// Separate subtest so an unavailable IPv6 host does not skip IPv4 evidence.
	t.Run("IPv6Server", func(t *testing.T) {
		v6 := localServer(t, "tcp6", "[::1]:0")
		get(t, "ipv6", v6.URL, nil, true)
		get(t, "auto", v6.URL, nil, true)
		get(t, "ipv4", v6.URL, nil, false)
	})
}

func TestProxyFamilyConstrainsLocalPeer(t *testing.T) {
	proxy := localServer(t, "tcp4", "127.0.0.1:0")
	u, _ := url.Parse(proxy.URL)
	// The IPv6 literal is carried to the HTTP proxy; local IPv4 is allowed.
	get(t, "ipv4", "http://[2001:db8::1]/file", http.ProxyURL(u), true)
	get(t, "ipv6", "http://example.invalid/file", http.ProxyURL(u), false)
}

func TestRedirectCannotEscapeFamily(t *testing.T) {
	entry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://[::1]:1/file", http.StatusFound)
	}))
	defer entry.Close()
	transport := Apply(&http.Transport{}, "ipv4")
	defer transport.CloseIdleConnections()
	c := &http.Client{Transport: transport, Timeout: time.Second}
	_, err := c.Get(entry.URL)
	if err == nil {
		t.Fatal("redirect unexpectedly succeeded")
	}
	// Exercise the redirect target without allowing a tcp6 fallback.
	if _, err := transport.DialContext(context.Background(), "tcp", "[::1]:1"); err == nil {
		t.Fatal("IPv4 dial accepted an IPv6 literal")
	}
}

func TestValidate(t *testing.T) {
	for _, family := range []string{"", "auto", "ipv4", "ipv6"} {
		if err := Validate(family); err != nil {
			t.Fatal(err)
		}
	}
	if Validate("prefer6") == nil {
		t.Fatal("unsupported family accepted")
	}
}
