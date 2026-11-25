package main

import (
	"strings"
	"testing"
)

func TestWatched(t *testing.T) {
	for _, name := range []string{"main.go", "0004.sql", "characters.json", ".env", "go.mod", "go.sum"} {
		if !watched(name) {
			t.Errorf("%s should trigger a rebuild", name)
		}
	}
	for _, name := range []string{"App.vue", "style.css", "package-lock.json", "README.md", "2026-12-01.dump"} {
		if watched(name) {
			t.Errorf("%s should not trigger a rebuild", name)
		}
	}
}

func TestFirstChange(t *testing.T) {
	base := map[string]stamp{"a.go": {mod: 1, size: 10}}

	if _, ok := firstChange(base, map[string]stamp{"a.go": {mod: 1, size: 10}}); ok {
		t.Fatal("identical snapshots reported a change")
	}
	if path, ok := firstChange(base, map[string]stamp{"a.go": {mod: 2, size: 10}}); !ok || path != "a.go" {
		t.Fatalf("saved file: %q, %v", path, ok)
	}
	if path, ok := firstChange(base, map[string]stamp{"a.go": {mod: 1, size: 11}}); !ok || path != "a.go" {
		t.Fatalf("resized file: %q, %v", path, ok)
	}
	if path, ok := firstChange(base, map[string]stamp{"a.go": {mod: 1, size: 10}, "b.go": {mod: 1, size: 1}}); !ok || path != "b.go" {
		t.Fatalf("new file: %q, %v", path, ok)
	}
	if path, ok := firstChange(base, map[string]stamp{}); !ok || path != "a.go" {
		t.Fatalf("deleted file: %q, %v", path, ok)
	}
}

// The walk runs from the repo root in real use; it must find the sources and
// stay out of Vite's tree, the embedded build and git.
func TestSnapshotSkipsBuildTrees(t *testing.T) {
	t.Chdir("..")
	files, err := snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"main.go", "go.mod", "characters.json", "store/migrate.go"} {
		if _, ok := files[want]; !ok {
			t.Errorf("snapshot misses %s", want)
		}
	}
	for path := range files {
		if strings.HasPrefix(path, "frontend/") || strings.HasPrefix(path, "web/") ||
			strings.HasPrefix(path, ".git/") || strings.HasPrefix(path, ".tmp/") ||
			strings.HasPrefix(path, "backups/") {
			t.Fatalf("snapshot walked %s", path)
		}
	}
}
