// Package snapshots saves named JSON reports (conformance, scenario,
// load, lifecycle, fuzz) under ./snapshots/ and diffs them: which
// checks changed, and which flipped pass->fail or fail->pass.
//
// Snapshots are plain files; the store keeps a small index.json next
// to them. Report shapes are read generically (see extractPoints), so
// new report kinds keep working as long as they carry checks or a
// summary object.
package snapshots

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Entry describes one saved snapshot.
type Entry struct {
	Name    string    `json:"name"`
	Kind    string    `json:"kind"`
	SavedAt time.Time `json:"saved_at"`
	File    string    `json:"file"`
	Summary string    `json:"summary,omitempty"`
}

// Store manages a snapshot directory.
type Store struct {
	dir string
}

// New returns a store rooted at dir; "" means ./snapshots.
func New(dir string) *Store {
	if dir == "" {
		dir = "snapshots"
	}
	return &Store{dir: dir}
}

// Dir returns the store root.
func (s *Store) Dir() string { return s.dir }

// sanitizeName keeps snapshots filesystem-safe.
func sanitizeName(name string) (string, error) {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-_")
	if out == "" {
		return "", fmt.Errorf("snapshots: name %q has no usable characters", name)
	}
	if len(out) > 80 {
		out = out[:80]
	}
	return out, nil
}

// Save validates data as JSON and stores it under name. An existing
// snapshot with the same name is overwritten (the index keeps the
// newest entry).
func (s *Store) Save(kind, name string, data json.RawMessage) (*Entry, error) {
	if !json.Valid(data) {
		return nil, fmt.Errorf("snapshots: data is not valid JSON")
	}
	safe, err := sanitizeName(name)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return nil, fmt.Errorf("snapshots: mkdir: %w", err)
	}
	file := safe + ".json"
	if err := os.WriteFile(filepath.Join(s.dir, file), pretty(data), 0o644); err != nil {
		return nil, fmt.Errorf("snapshots: write: %w", err)
	}
	entry := &Entry{
		Name:    safe,
		Kind:    kind,
		SavedAt: time.Now().UTC(),
		File:    file,
		Summary: summarize(kind, data),
	}
	idx := s.loadIndex()
	kept := idx[:0]
	for _, e := range idx {
		if e.Name != safe {
			kept = append(kept, e)
		}
	}
	idx = append(kept, entry)
	if err := s.writeIndex(idx); err != nil {
		return nil, err
	}
	return entry, nil
}

// List returns all snapshots, newest first.
func (s *Store) List() ([]*Entry, error) {
	idx := s.loadIndex()
	out := make([]*Entry, len(idx))
	copy(out, idx)
	sort.Slice(out, func(i, j int) bool { return out[i].SavedAt.After(out[j].SavedAt) })
	return out, nil
}

// Get returns the raw report JSON for name.
func (s *Store) Get(name string) (json.RawMessage, *Entry, error) {
	safe, err := sanitizeName(name)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range s.loadIndex() {
		if e.Name == safe {
			raw, err := os.ReadFile(filepath.Join(s.dir, e.File))
			if err != nil {
				return nil, nil, fmt.Errorf("snapshots: read: %w", err)
			}
			return raw, e, nil
		}
	}
	return nil, nil, fmt.Errorf("snapshots: %q not found", name)
}

// Delete removes a snapshot and its index entry.
func (s *Store) Delete(name string) error {
	safe, err := sanitizeName(name)
	if err != nil {
		return err
	}
	idx := s.loadIndex()
	kept := idx[:0]
	found := false
	for _, e := range idx {
		if e.Name == safe {
			found = true
			_ = os.Remove(filepath.Join(s.dir, e.File))
			continue
		}
		kept = append(kept, e)
	}
	if !found {
		return fmt.Errorf("snapshots: %q not found", name)
	}
	return s.writeIndex(kept)
}

func (s *Store) indexPath() string { return filepath.Join(s.dir, "index.json") }

func (s *Store) loadIndex() []*Entry {
	raw, err := os.ReadFile(s.indexPath())
	if err != nil {
		return nil
	}
	var idx []*Entry
	if err := json.Unmarshal(raw, &idx); err != nil {
		return nil
	}
	return idx
}

func (s *Store) writeIndex(idx []*Entry) error {
	if idx == nil {
		idx = []*Entry{}
	}
	raw, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.indexPath(), raw, 0o644)
}

func pretty(data json.RawMessage) []byte {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return data
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return data
	}
	return out
}

// summarize extracts a one-line human summary per report kind.
func summarize(kind string, data json.RawMessage) string {
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return ""
	}
	switch kind {
	case "conformance", "lifecycle":
		pass, total := countChecks(doc)
		return fmt.Sprintf("%d/%d checks passed", pass, total)
	case "scenario":
		pass, total := countScenarios(doc)
		return fmt.Sprintf("%d/%d scenarios passed", pass, total)
	case "fuzz":
		if f, ok := doc["findings"].([]any); ok {
			if len(f) == 0 {
				return "clean — no findings"
			}
			return fmt.Sprintf("%d findings", len(f))
		}
	case "load":
		if sum, ok := doc["summary"].(map[string]any); ok {
			return fmt.Sprintf("p50=%v p95=%v err=%v", sum["p50_ms"], sum["p95_ms"], sum["errors"])
		}
	}
	return fmt.Sprintf("%d bytes", len(data))
}

func countChecks(doc map[string]any) (pass, total int) {
	for _, p := range extractPoints(doc) {
		total++
		if p.status == "pass" || p.status == "passed" {
			pass++
		}
	}
	return pass, total
}

func countScenarios(doc map[string]any) (pass, total int) {
	scs, _ := doc["scenarios"].([]any)
	for _, sc := range scs {
		m, _ := sc.(map[string]any)
		if m == nil {
			continue
		}
		total++
		if b, _ := m["passed"].(bool); b {
			pass++
		}
	}
	return pass, total
}
