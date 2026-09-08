package mounter

import "testing"

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
