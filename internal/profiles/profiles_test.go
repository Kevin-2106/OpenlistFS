package profiles

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStagedSavePreservesExistingProfilesAndOptions(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "profiles.json")
	if err := os.WriteFile(destination, []byte(`{"active":"old"}`), 0600); err != nil {
		t.Fatal(err)
	}
	d := Document{Store: Store{Active: "new", Profiles: map[string]Profile{"new": {URL: "https://example.test", Options: &Options{Backend: "s3", Writable: true}}}}}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeDocument(destination, raw); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	var got Document
	if err := json.Unmarshal(actual, &got); err != nil {
		t.Fatal(err)
	}
	if got.Active != "new" || got.Profiles["new"].Options.Backend != "s3" || !got.Profiles["new"].Options.Writable {
		t.Fatal("saved configuration changed")
	}
	files, err := os.ReadDir(filepath.Dir(destination))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatal("staging file was left behind")
	}
}
