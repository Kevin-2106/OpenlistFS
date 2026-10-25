// Package profilecode implements versioned, portable server-configuration codes.
// Base64 is transport encoding, not encryption: codes contain credentials.
package profilecode

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"openlistfs/internal/netpolicy"
	"openlistfs/internal/profiles"
	"openlistfs/internal/proxy"
)

const Prefix = "openlistfs:v1:"
const MaxCodeSize = 64 << 10

type Configuration struct {
	Version int              `json:"version"`
	Name    string           `json:"name"`
	Profile profiles.Profile `json:"profile"`
}

func Encode(name string, profile profiles.Profile) (string, error) {
	c := Configuration{Version: 1, Name: strings.TrimSpace(name), Profile: profile}
	if err := validate(c); err != nil {
		return "", err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("cannot encode configuration")
	}
	code := Prefix + base64.RawURLEncoding.EncodeToString(data)
	if len(code) > MaxCodeSize {
		return "", fmt.Errorf("configuration code is too large (maximum 64 KiB)")
	}
	return code, nil
}

func Decode(code string) (Configuration, error) {
	var c Configuration
	if len(code) > MaxCodeSize {
		return c, fmt.Errorf("configuration code is too large (maximum 64 KiB)")
	}
	code = strings.TrimSpace(code)
	if !strings.HasPrefix(code, Prefix) {
		return c, fmt.Errorf("unsupported configuration code; expected openlistfs:v1:")
	}
	encoded := strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, strings.TrimPrefix(code, Prefix))
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return c, fmt.Errorf("configuration code has invalid Base64 data")
	}
	if !utf8.Valid(data) {
		return c, fmt.Errorf("configuration code has invalid text")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return Configuration{}, fmt.Errorf("configuration code has invalid JSON or unsupported fields")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return Configuration{}, fmt.Errorf("configuration code has trailing data")
	}
	c.Name = strings.TrimSpace(c.Name)
	if err := validate(c); err != nil {
		return Configuration{}, err
	}
	return c, nil
}

func validate(c Configuration) error {
	if c.Version != 1 {
		return fmt.Errorf("unsupported configuration-code version")
	}
	if c.Name == "" || utf8.RuneCountInString(c.Name) > 128 || strings.ContainsAny(c.Name, "\x00\r\n") {
		return fmt.Errorf("configuration name must contain 1–128 characters on one line")
	}
	u, err := url.Parse(c.Profile.URL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.Fragment != "" {
		return fmt.Errorf("server address must be an HTTP or HTTPS URL")
	}
	if c.Profile.Options == nil {
		return nil
	}
	o := c.Profile.Options
	if err := netpolicy.Validate(o.IPFamily); err != nil {
		return err
	}
	if len(o.Drive) != 2 || o.Drive[1] != ':' || !((o.Drive[0] >= 'A' && o.Drive[0] <= 'Z') || (o.Drive[0] >= 'a' && o.Drive[0] <= 'z')) {
		return fmt.Errorf("mount drive must be a drive letter such as O:")
	}
	if o.Backend != "" && o.Backend != "api" && o.Backend != "webdav" && o.Backend != "s3" {
		return fmt.Errorf("unsupported storage backend")
	}
	if o.ReadAheadMiB < 0 || o.ReadAheadMiB > 4096 || o.ChunkKiB < 64 || o.ChunkKiB > 8192 || o.CacheMiB < 16 || o.CacheMiB > 16384 || o.Workers < 1 || o.Workers > 32 || o.TTLMinutes < 1 || o.TTLMinutes > 1440 {
		return fmt.Errorf("mount options contain values outside supported limits")
	}
	if _, _, err := proxy.Parse(o.Proxy); err != nil {
		return fmt.Errorf("configuration contains an invalid proxy setting")
	}
	return nil
}

// Add inserts a new profile and selects it, without overwriting existing names.
// It has no network, startup, or mount side effects.
func Add(document profiles.Document, c Configuration) (profiles.Document, string, error) {
	if err := validate(c); err != nil {
		return document, "", err
	}
	next := document
	next.Profiles = make(map[string]profiles.Profile, len(document.Profiles)+1)
	for name, p := range document.Profiles {
		next.Profiles[name] = p
	}
	name := c.Name
	for suffix := 2; ; suffix++ {
		if _, exists := next.Profiles[name]; !exists {
			break
		}
		ending := " (" + strconv.Itoa(suffix) + ")"
		base := []rune(c.Name)
		if len(base)+len(ending) > 128 {
			base = base[:128-len(ending)]
		}
		name = string(base) + ending
	}
	next.Profiles[name] = c.Profile
	next.Active = name
	if c.Profile.Options != nil {
		next.Options = *c.Profile.Options
	}
	return next, name, nil
}
