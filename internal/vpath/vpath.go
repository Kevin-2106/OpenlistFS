// Package vpath normalizes virtual paths inside OpenListFS.
//
// The canonical form is absolute and slash-separated ("/Movies/A.mkv"); root
// is "/". WinFsp/gofs hands us backslash paths ("\Movies\A.mkv"); OpenList
// wants slash paths. All conversions funnel through here so no caller does
// ad-hoc string munging.
package vpath

import (
	"path"
	"strings"
)

// FromWin converts a Windows-style virtual path (backslashes, optional
// leading backslash, possibly unclean) into the canonical form.
func FromWin(p string) string {
	return Clean(strings.ReplaceAll(p, "\\", "/"))
}

// Clean converts any slash path into the canonical form. The result is
// always lexically contained under root: ".." and "." are resolved, duplicate
// slashes collapse, trailing slashes are dropped.
func Clean(p string) string {
	c := path.Clean("/" + strings.ReplaceAll(p, "\\", "/"))
	if c == "/" {
		return "/"
	}
	return c
}

// IsRoot reports whether p is the root directory.
func IsRoot(p string) bool {
	return Clean(p) == "/"
}

// Base returns the final element of the canonical path; "/" at root.
func Base(p string) string {
	c := Clean(p)
	if c == "/" {
		return "/"
	}
	return path.Base(c)
}

// Dir returns the canonical parent directory of p ("/" for top-level entries).
func Dir(p string) string {
	c := Clean(p)
	if c == "/" {
		return "/"
	}
	return path.Dir(c)
}

// Join appends elements to a canonical path and returns the canonical result.
func Join(p string, elem ...string) string {
	return Clean(p + "/" + strings.Join(elem, "/"))
}
