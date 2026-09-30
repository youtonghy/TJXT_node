package conf

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const validConfig = `{"Nodes":[{"ApiHost":"http://panel","NodeID":1,"ApiKey":"k"}]}`

func TestWatchSurvivesRenameAndSkipsBrokenFiles(t *testing.T) {
	watchDebounce = 50 * time.Millisecond
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(validConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	reloads := make(chan struct{}, 10)
	w, err := Watch(path, func() { reloads <- struct{}{} })
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer func() {
		if err := w.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()

	replace := func(content string) {
		t.Helper()
		tmp := filepath.Join(dir, "config.json.tmp")
		if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, path); err != nil {
			t.Fatal(err)
		}
	}
	expectReload := func(want bool) {
		t.Helper()
		select {
		case <-reloads:
			if !want {
				t.Fatal("unexpected reload")
			}
		case <-time.After(time.Second):
			if want {
				t.Fatal("expected a reload")
			}
		}
	}

	// An editor-style atomic replace triggers a reload, twice in a row.
	replace(validConfig)
	expectReload(true)
	replace(validConfig)
	expectReload(true)
	// A file that does not parse (or validate) does not.
	replace(`{"Nodes": [`)
	expectReload(false)
	replace(`{"Nodes": []}`)
	expectReload(false)
	// Other files in the directory are ignored.
	if err := os.WriteFile(filepath.Join(dir, "other.json"), []byte(validConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	expectReload(false)
}

func TestWatchCloseIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	w, err := Watch(path, func() {})
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}
