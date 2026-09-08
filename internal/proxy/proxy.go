// Package proxy resolves the user-facing proxy setting — "none", "system"
// or a specific proxy URL — into the http.Transport Proxy function shared
// by the API client and the range readers, so one setting covers both the
// JSON API traffic and the direct-URL downloads.
package proxy

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// ProxyFunc decides the proxy for one request; a nil URL means direct.
type ProxyFunc = func(*http.Request) (*url.URL, error)

// Setting values with special meaning; anything else is a proxy URL.
const (
	ModeNone   = "none"
	ModeSystem = "system"
)

// Parse interprets the proxy setting:
//
//	"" or "none"    direct connections (the historical default)
//	"system"        the Windows per-user system proxy (the WinINET
//	                registry settings proxies like Clash register), falling
//	                back to HTTP(S)_PROXY/_PROXY environment variables
//	anything else   a specific proxy URL; schemes http/https/socks5 are
//	                accepted and a bare "host:port" is treated as http://
//
// The returned description is for logs; it never contains credentials.
func Parse(setting string) (ProxyFunc, string, error) {
	switch s := strings.ToLower(strings.TrimSpace(setting)); {
	case s == "" || s == ModeNone:
		return nil, "direct", nil
	case s == ModeSystem:
		fn, desc := system()
		return fn, desc, nil
	default:
		u, err := parseURL(setting)
		if err != nil {
			return nil, "", fmt.Errorf("proxy: %w", err)
		}
		return http.ProxyURL(u), "specific " + sanitize(u), nil
	}
}

// sanitize strips userinfo from a proxy URL for logging.
func sanitize(u *url.URL) string {
	if u.User == nil {
		return u.String()
	}
	c := *u
	c.User = url.User(u.User.Username())
	return c.String()
}

func parseURL(setting string) (*url.URL, error) {
	s := strings.TrimSpace(setting)
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %v", setting, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("unsupported scheme %q in %q (want http, https or socks5)", u.Scheme, setting)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("missing host in %q", setting)
	}
	return u, nil
}

// system returns the Windows system proxy function. The WinINET per-user
// settings are what the Settings app and proxy clients (Clash, v2rayN…)
// write, so they are the truthful "system proxy" on Windows; the standard
// library's environment lookup is the fallback for headless setups.
func system() (ProxyFunc, string) {
	if servers, ok := registryProxy(); ok {
		return registryFunc(servers), "system (registry: " + describe(servers) + ")"
	}
	return http.ProxyFromEnvironment, "system (environment)"
}

// registryProxy reads the enabled per-user proxy; ok is false when the
// proxy is disabled or unreadable.
func registryProxy() (servers map[string]string, ok bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`,
		registry.QUERY_VALUE)
	if err != nil {
		return nil, false
	}
	defer k.Close()
	enabled, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enabled == 0 {
		return nil, false
	}
	raw, _, err := k.GetStringValue("ProxyServer")
	if err != nil || strings.TrimSpace(raw) == "" {
		return nil, false
	}
	servers = parseServers(raw)
	return servers, len(servers) > 0
}

// parseServers handles both ProxyServer shapes seen in the registry:
//
//	"host:port"                    one proxy for every protocol
//	"http=h:p;https=h:p;socks=h:p" per-protocol entries
//
// The server-wide value is stored under key "".
func parseServers(raw string) map[string]string {
	servers := make(map[string]string)
	if !strings.Contains(raw, "=") {
		if s := strings.TrimSpace(raw); s != "" {
			servers[""] = s
		}
		return servers
	}
	for _, part := range strings.Split(raw, ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
			if v = strings.TrimSpace(v); v != "" {
				servers[k] = v
			}
		}
	}
	return servers
}

// registryFunc maps a request scheme to a registry entry: an exact
// protocol entry wins over the server-wide value, and a socks= entry
// proxies schemes the server did not list. Unlisted means direct.
func registryFunc(servers map[string]string) ProxyFunc {
	return func(req *http.Request) (*url.URL, error) {
		hostport := servers[""]
		if h, ok := servers[req.URL.Scheme]; ok {
			hostport = h
		}
		if hostport == "" {
			if s, ok := servers["socks"]; ok {
				return &url.URL{Scheme: "socks5", Host: s}, nil
			}
			return nil, nil
		}
		return &url.URL{Scheme: "http", Host: hostport}, nil
	}
}

func describe(servers map[string]string) string {
	if s, ok := servers[""]; ok && len(servers) == 1 {
		return s
	}
	parts := make([]string, 0, len(servers))
	for _, k := range []string{"http", "https", "ftp", "socks"} {
		if v, ok := servers[k]; ok {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, ";")
}
