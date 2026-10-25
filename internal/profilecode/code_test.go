package profilecode

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"

	"openlistfs/internal/profiles"
)

func sample() Configuration {
	return Configuration{Version: 1, Name: "家里的存储", Profile: profiles.Profile{
		URL: "https://storage.example.test/media?region=us-east-1", Username: "测试用户", Password: "p@ss:+/中文", Token: "test-token",
		Options: &profiles.Options{Drive: "O:", Network: true, ReadAheadMiB: 64, ChunkKiB: 512, CacheMiB: 256, Workers: 3, TTLMinutes: 2, Backend: "s3", Proxy: "http://127.0.0.1:7897", Writable: true, IPFamily: "ipv6"},
	}}
}

func TestConfigurationRoundTrip(t *testing.T) {
	c := sample()
	code, err := Encode(c.Name, c.Profile)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(" \n" + code + "\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, c) {
		t.Fatalf("configuration changed in round trip")
	}
	// Wrapped clipboard text remains usable.
	wrapped := Prefix + strings.Join(strings.Split(strings.TrimPrefix(code, Prefix), ""), "\n")
	if _, err := Decode(wrapped); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidCodesAndOptionsAreRejected(t *testing.T) {
	encodeJSON := func(s string) string { return Prefix + base64.RawURLEncoding.EncodeToString([]byte(s)) }
	codes := []string{"", "openlistfs:v2:data", Prefix + "%%%", strings.Repeat("x", MaxCodeSize+1),
		encodeJSON(`{"version":2,"name":"x","profile":{"url":"https://example.test"}}`),
		encodeJSON(`{"version":1,"name":"x","profile":{"url":"file:///secret"}}`),
		encodeJSON(`{"version":1,"name":"x","profile":{"url":"https://example.test","autoMount":true}}`),
		encodeJSON(`{"version":1,"name":"x","profile":{"url":"https://example.test"}} {}`),
		Prefix + base64.RawURLEncoding.EncodeToString([]byte{0xff, 0xfe}),
	}
	for _, code := range codes {
		if _, err := Decode(code); err == nil {
			t.Fatal("invalid code was accepted")
		}
	}
	c := sample()
	c.Profile.Options.CacheMiB = 1 << 30
	if _, err := Encode(c.Name, c.Profile); err == nil {
		t.Fatal("unbounded cache setting accepted")
	}
	c = sample()
	c.Profile.Options.Backend = "shell"
	if _, err := Encode(c.Name, c.Profile); err == nil {
		t.Fatal("unknown backend accepted")
	}
}

func TestImportAddsWithoutOverwritingOrChangingStartup(t *testing.T) {
	c := sample()
	old := profiles.Profile{URL: "https://original.example.test", Password: "original"}
	d := profiles.Document{Store: profiles.Store{Active: "existing", AutoMount: false, Profiles: map[string]profiles.Profile{c.Name: old, c.Name + " (2)": old}}}
	next, name, err := Add(d, c)
	if err != nil {
		t.Fatal(err)
	}
	if name != c.Name+" (3)" || next.Active != name {
		t.Fatalf("unexpected imported name/selection: %q", name)
	}
	if len(d.Profiles) != 2 || d.Active != "existing" || d.Profiles[c.Name].Password != "original" {
		t.Fatal("original document changed")
	}
	if next.AutoMount || next.Options.Backend != "s3" || next.Profiles[c.Name].Password != "original" {
		t.Fatal("import changed startup or overwrote existing profile")
	}
}

func TestInvalidIPFamilyIsRejected(t *testing.T) {
	c := sample()
	c.Profile.Options.IPFamily = "prefer6"
	if _, err := Encode(c.Name, c.Profile); err == nil {
		t.Fatal("invalid family accepted")
	}
	c.Profile.Options.IPFamily = ""
	code, err := Encode(c.Name, c.Profile)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(code)
	if err != nil || decoded.Profile.Options.IPFamily != "" {
		t.Fatal("legacy empty family must remain valid", err)
	}
}
