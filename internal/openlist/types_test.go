package openlist

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEntryUnmarshalTimestampShapes(t *testing.T) {
	// Live server shape: RFC3339 strings with nanoseconds, numeric size.
	var a Entry
	err := json.Unmarshal([]byte(`{
		"name": "miriya", "size": 0, "is_dir": true,
		"modified": "2026-04-08T13:55:33.837921512+09:00",
		"created": "2026-04-08T13:55:33.837921512+09:00",
		"sign": ""
	}`), &a)
	if err != nil {
		t.Fatalf("live shape: %v", err)
	}
	want := time.Date(2026, 4, 8, 13, 55, 33, 0, time.FixedZone("", 9*3600)).Unix()
	if a.Modified != want {
		t.Errorf("Modified = %d, want %d", a.Modified, want)
	}

	// Classic shape: unix numbers.
	var b Entry
	err = json.Unmarshal([]byte(`{"name":"A.mkv","size":1073741824,"is_dir":false,"modified":1700000000}`), &b)
	if err != nil {
		t.Fatalf("numeric shape: %v", err)
	}
	if b.Modified != 1700000000 || b.Size != 1073741824 {
		t.Errorf("numeric shape decoded wrong: %+v", b)
	}

	// Defensive shape: numeric strings and empty strings.
	var c Entry
	err = json.Unmarshal([]byte(`{"name":"B","size":"42","modified":"","created":null}`), &c)
	if err != nil {
		t.Fatalf("string shape: %v", err)
	}
	if c.Size != 42 || c.Modified != 0 {
		t.Errorf("string shape decoded wrong: %+v", c)
	}

	// Garbage must still error, not silently zero.
	var d Entry
	err = json.Unmarshal([]byte(`{"modified":"not-a-date"}`), &d)
	if err == nil {
		t.Error("expected error for garbage timestamp")
	}
}
