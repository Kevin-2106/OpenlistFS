// Package openlist is a thin client for the OpenList native HTTP API.
//
// All OpenList-specific behavior — endpoints, JSON shapes, authentication,
// direct-link resolution — lives here. Callers see plain values (Entry, Link)
// and errors that never contain credentials or signed URL parameters.
package openlist

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Entry describes a file or directory as reported by the server.
type Entry struct {
	Name     string
	Size     int64
	IsDir    bool
	Modified int64 // unix seconds
	Created  int64 // unix seconds
	Sign     string
}

// flexInt64 accepts a JSON number, a numeric string, or an RFC3339 timestamp
// string. OpenList returns sizes as numbers and timestamps as RFC3339 strings
// (with nanoseconds), but the shapes have drifted between versions.
type flexInt64 int64

func (f *flexInt64) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*f = 0
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			*f = 0
			return nil
		}
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			*f = flexInt64(v)
			return nil
		}
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			*f = flexInt64(t.Unix())
			return nil
		}
		return fmt.Errorf("cannot parse %q as number or timestamp", s)
	}
	var v int64
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*f = flexInt64(v)
	return nil
}

type entryShadow struct {
	Name     string    `json:"name"`
	Size     flexInt64 `json:"size"`
	IsDir    bool      `json:"is_dir"`
	Modified flexInt64 `json:"modified"`
	Created  flexInt64 `json:"created"`
	Sign     string    `json:"sign"`
}

// UnmarshalJSON decodes both known shapes of OpenList objects.
func (e *Entry) UnmarshalJSON(b []byte) error {
	var s entryShadow
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*e = s.toEntry()
	return nil
}

func (s entryShadow) toEntry() Entry {
	return Entry{
		Name:     s.Name,
		Size:     int64(s.Size),
		IsDir:    s.IsDir,
		Modified: int64(s.Modified),
		Created:  int64(s.Created),
		Sign:     s.Sign,
	}
}

// Link is a resolved, directly fetchable URL for one file.
type Link struct {
	// URL is the direct link, already percent-encoded; use as-is.
	URL string
	// Header holds extra HTTP headers the storage requires on fetches
	// (cookies, referer, ...). May be empty.
	Header map[string]string
	// Source names the API that produced the link ("fs/link" or "fs/get").
	Source string
	// ResolvedAt is when the link was obtained; remote URLs may expire.
	ResolvedAt time.Time
}

// ServerInfo carries what probe reports about the remote.
type ServerInfo struct {
	Title   string `json:"site_title"`
	Version string `json:"version"`
}
