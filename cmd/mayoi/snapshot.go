package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"mayoi/internal/workspace"
)

// wsSnapshot is a copy of a folder taken before the agent runs, of every
// file it had: the content of small files outright, and just their names
// for the rest. Reverting restores what the agent changed and removes
// what it created.
type wsSnapshot struct {
	// files maps a path relative to the root to its content, nil for a
	// large file we only noted. Path separator is always '/'.
	files map[string][]byte
	// existed reports whether the file was there before the turn.
	existed map[string]bool
	// over reports that a cap was hit; nothing of the snapshot is known
	// reliable, so revert is disabled and the pane says so.
	over bool
}

const (
	snapshotMaxFile  = 4 << 20  // 4 MiB per file
	snapshotMaxTotal = 96 << 20 // 96 MiB of content, whole snapshot
)

func newSnapshot(root string) *wsSnapshot {
	s := &wsSnapshot{files: map[string][]byte{}, existed: map[string]bool{}}
	total := int64(0)
	// Directory that is not worth walking into: the same set the file
	// index skips.
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == string(filepath.Separator) {
				return nil
			}
			if workspace.Skipped(name) {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if info.Size() > snapshotMaxFile {
			s.existed[rel] = true
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		total += int64(len(data))
		if total > snapshotMaxTotal {
			s.over = true
			return fs.SkipAll
		}
		s.files[rel] = data
		s.existed[rel] = true
		return nil
	})
	return s
}

// wsChange is one path the agent touched, with the kind of change it
// made.
type wsChange struct {
	Path   string // relative to the root
	Status string // "added" | "modified" | "deleted"
}

// changed lists the files whose content differs from the snapshot, and
// the files that are there now but were not then.
func (s *wsSnapshot) changed(root string) []wsChange {
	var out []wsChange
	if s.over {
		return nil
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if workspace.Skipped(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if before, ok := s.files[rel]; !ok {
			out = append(out, wsChange{rel, "added"})
			return nil
		} else if string(before) != string(data) {
			out = append(out, wsChange{rel, "modified"})
		}
		return nil
	})
	for rel := range s.existed {
		if _, ok := s.files[rel]; !ok { // large file, treat as gone only when it is
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
				out = append(out, wsChange{rel, "deleted"})
			}
			continue
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			out = append(out, wsChange{rel, "deleted"})
		}
	}
	slices.SortFunc(out, func(x, y wsChange) int { return strings.Compare(x.Path, y.Path) })
	return out
}

// restore puts the files the agent touched back to what they were.
// Files the agent created are removed; files it changed are written
// with their former content.
func (s *wsSnapshot) restore(root string) error {
	if s.over {
		return fmt.Errorf("the snapshot hit a limit, so reverting is off")
	}
	var firstErr error
	for rel, data := range s.files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := os.WriteFile(path, data, 0o644); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// Deleting the files the agent added: gone from s.files, present in
	// the walk again.
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if _, ok := s.files[rel]; !ok && !s.existed[rel] {
			_ = os.Remove(path)
		}
		return nil
	})
	return firstErr
}

// count is for banners: how many changes of each kind.
func changeKinds(changes []wsChange) map[string]int {
	counts := map[string]int{}
	for _, c := range changes {
		counts[c.Status]++
	}
	_ = strconv.Itoa
	return counts
}
