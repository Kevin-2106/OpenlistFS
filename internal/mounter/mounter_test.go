package mounter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestStartChecksWinFspBeforeRemoteOrProxyWork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	missing := errors.New("WinFsp is missing; run the OpenListFS installer")
	checked := false
	session, err := start(context.Background(), Config{
		Mountpoint: "O:", URL: server.URL, Username: "test", Password: "test",
		Proxy: "://invalid proxy",
	}, func() error {
		checked = true
		return missing
	})
	if !checked || session != nil || !errors.Is(err, missing) {
		t.Fatalf("start = %v, %v; dependency checked = %v", session, err, checked)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("unready mount made %d remote requests", got)
	}
}

func TestDavBaseURL(t *testing.T) {
	cases := map[string]string{
		// OpenList convention: bare server URL gets /dav appended.
		"https://example.com":     "https://example.com/dav",
		"https://example.com/":    "https://example.com/dav",
		"http://example.com:5244": "http://example.com:5244/dav",
		// A URL that already carries a path is taken as-is.
		"https://example.com/dav":           "https://example.com/dav",
		"https://example.com/dav/":          "https://example.com/dav",
		"https://ex.com/remote.php/webdav/": "https://ex.com/remote.php/webdav",
	}
	for in, want := range cases {
		if got := davBaseURL(in); got != want {
			t.Errorf("davBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}
