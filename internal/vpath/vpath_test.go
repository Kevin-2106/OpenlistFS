package vpath

import "testing"

func TestClean(t *testing.T) {
	cases := map[string]string{
		"":                "/",
		"/":               "/",
		"Movies":          "/Movies",
		"/Movies":         "/Movies",
		"/Movies/":        "/Movies",
		"//Movies//A":     "/Movies/A",
		"/a/./b":          "/a/b",
		"/a/b/../c":       "/a/c",
		"/../../escape":   "/escape",
		"\\Movies\\A.mkv": "/Movies/A.mkv",
		"/\\mixed/path":   "/mixed/path",
	}
	for in, want := range cases {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFromWin(t *testing.T) {
	cases := map[string]string{
		"\\":              "/",
		"\\Movies":        "/Movies",
		"\\Movies\\A.mkv": "/Movies/A.mkv",
		"Movies\\Sub":     "/Movies/Sub",
		"\\\\server\\x":   "/server/x",
		"\\a\\..\\b":      "/b",
	}
	for in, want := range cases {
		if got := FromWin(in); got != want {
			t.Errorf("FromWin(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParts(t *testing.T) {
	if !IsRoot("/") || IsRoot("/x") {
		t.Error("IsRoot broken")
	}
	if got := Base("/Movies/A.mkv"); got != "A.mkv" {
		t.Errorf("Base = %q", got)
	}
	if got := Base("/"); got != "/" {
		t.Errorf("Base(/) = %q", got)
	}
	if got := Dir("/Movies/A.mkv"); got != "/Movies" {
		t.Errorf("Dir = %q", got)
	}
	if got := Dir("/A.mkv"); got != "/" {
		t.Errorf("Dir = %q", got)
	}
	if got := Join("/Movies", "B.mkv"); got != "/Movies/B.mkv" {
		t.Errorf("Join = %q", got)
	}
}
