package proxy

import (
	"net/http"
	"net/url"
	"testing"
)

func TestParseNoneAndEmpty(t *testing.T) {
	for _, setting := range []string{"", "   ", "none", "NONE", " None "} {
		fn, desc, err := Parse(setting)
		if err != nil {
			t.Fatalf("Parse(%q): %v", setting, err)
		}
		if fn != nil {
			t.Errorf("Parse(%q): want nil proxy func", setting)
		}
		if desc != "direct" {
			t.Errorf("Parse(%q): desc = %q, want direct", setting, desc)
		}
	}
}

func TestParseSpecificURL(t *testing.T) {
	cases := []struct {
		setting string
		want    string
	}{
		{"127.0.0.1:7897", "http://127.0.0.1:7897"},
		{"http://127.0.0.1:7897", "http://127.0.0.1:7897"},
		{"HTTP://Proxy.Example:8080", "http://Proxy.Example:8080"},
		{"socks5://127.0.0.1:1080", "socks5://127.0.0.1:1080"},
	}
	for _, tc := range cases {
		fn, _, err := Parse(tc.setting)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.setting, err)
		}
		if fn == nil {
			t.Fatalf("Parse(%q): nil proxy func", tc.setting)
		}
		u, err := fn(&http.Request{URL: &url.URL{Scheme: "https", Host: "files.example"}})
		if err != nil {
			t.Fatalf("Parse(%q) call: %v", tc.setting, err)
		}
		if u.String() != tc.want {
			t.Errorf("Parse(%q) = %q, want %q", tc.setting, u, tc.want)
		}
	}
}

func TestParseInvalid(t *testing.T) {
	for _, setting := range []string{"ftp://127.0.0.1:21", "http://", "://"} {
		if _, _, err := Parse(setting); err == nil {
			t.Errorf("Parse(%q): want error", setting)
		}
	}
}

func TestParseSystemNeverErrors(t *testing.T) {
	fn, desc, err := Parse("system")
	if err != nil {
		t.Fatalf("Parse(system): %v", err)
	}
	if fn == nil {
		// "system" with no registry proxy and no env vars resolves to
		// ProxyFromEnvironment, which is non-nil; nil would mean we lost
		// the setting entirely.
		t.Fatal("Parse(system): nil proxy func")
	}
	if len(desc) < 6 || desc[:6] != "system" {
		t.Errorf("desc = %q, want system…", desc)
	}
}

func TestParseServers(t *testing.T) {
	s := parseServers(" 127.0.0.1:7897 ")
	if len(s) != 1 || s[""] != "127.0.0.1:7897" {
		t.Errorf("server-wide shape: got %v", s)
	}
	s = parseServers("http=a:1; https=b:2;;socks=c:3")
	want := map[string]string{"http": "a:1", "https": "b:2", "socks": "c:3"}
	if len(s) != len(want) {
		t.Fatalf("per-protocol shape: got %v", s)
	}
	for k, v := range want {
		if s[k] != v {
			t.Errorf("s[%q] = %q, want %q", k, s[k], v)
		}
	}
	// Unrecognizable values are kept under "" verbatim; a bad host fails
	// at dial time with a clear error instead of silently going direct.
	if got := parseServers("not a server spec"); got[""] != "not a server spec" {
		t.Errorf("garbage input: got %v", got)
	}
}

func TestRegistryFuncLookup(t *testing.T) {
	get := func(fn ProxyFunc, scheme string) string {
		t.Helper()
		u, err := fn(&http.Request{URL: &url.URL{Scheme: scheme, Host: "files.example"}})
		if err != nil {
			t.Fatalf("call: %v", err)
		}
		if u == nil {
			return ""
		}
		return u.String()
	}
	perProto := registryFunc(map[string]string{"http": "a:1", "https": "b:2"})
	if got := get(perProto, "https"); got != "http://b:2" {
		t.Errorf("https = %q, want http://b:2", got)
	}
	if got := get(perProto, "http"); got != "http://a:1" {
		t.Errorf("http = %q, want http://a:1", got)
	}
	// No entry for the scheme and no socks fallback: direct.
	if got := get(perProto, "ftp"); got != "" {
		t.Errorf("ftp = %q, want direct", got)
	}
	serverWide := registryFunc(map[string]string{"": "p:1"})
	if got := get(serverWide, "https"); got != "http://p:1" {
		t.Errorf("server-wide https = %q, want http://p:1", got)
	}
	socksOnly := registryFunc(map[string]string{"socks": "s:2"})
	if got := get(socksOnly, "https"); got != "socks5://s:2" {
		t.Errorf("socks-only https = %q, want socks5://s:2", got)
	}
}
