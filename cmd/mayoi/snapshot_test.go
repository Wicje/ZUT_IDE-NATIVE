package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotChangedAndRevert(t *testing.T) {
	root := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("keep/keep.txt", "unchanged")
	write("change/a.txt", "before")
	write("delete/b.txt", "gone")

	snap := newSnapshot(root)
	if snap == nil {
		t.Fatal("no snapshot")
	}
	// Agent turn: modify a, delete b, add new.
	write("change/a.txt", "after")
	if err := os.Remove(filepath.Join(root, "delete/b.txt")); err != nil {
		t.Fatal(err)
	}
	write("new/c.txt", "brand new")
	changes := snap.changed(root)
	byPath := map[string]string{}
	for _, c := range changes {
		byPath[c.Path] = c.Status
	}
	if byPath["change/a.txt"] != "modified" {
		t.Errorf("a.txt status = %q, want modified (all: %v)", byPath["change/a.txt"], byPath)
	}
	if byPath["delete/b.txt"] != "deleted" {
		t.Errorf("b.txt status = %q, want deleted (all: %v)", byPath["delete/b.txt"], byPath)
	}
	if byPath["new/c.txt"] != "added" {
		t.Errorf("c.txt status = %q, want added (all: %v)", byPath["new/c.txt"], byPath)
	}
	if _, changed := byPath["keep/keep.txt"]; changed {
		t.Errorf("keep.txt reported as changed: %v", byPath)
	}

	if err := snap.restore(root); err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return ""
		}
		return string(b)
	}
	if got := read("change/a.txt"); got != "before" {
		t.Errorf("a.txt after revert = %q, want before", got)
	}
	if got := read("delete/b.txt"); got != "gone" {
		t.Errorf("b.txt after revert = %q, want gone", got)
	}
	if _, err := os.Stat(filepath.Join(root, "new/c.txt")); err == nil {
		t.Error("c.txt should be removed by revert")
	}
	if got := read("keep/keep.txt"); got != "unchanged" {
		t.Errorf("keep.txt after revert = %q, want unchanged", got)
	}
}
