package scenarios

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed testdata/packs/*.yaml
var builtinPacksFS embed.FS

// PackMeta describes a built-in pack for listings.
type PackMeta struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	ScenarioCount int      `json:"scenario_count"`
	Scenarios     []string `json:"scenarios"`
}

// builtinPackIDs are the embedded pack file stems, in presentation order.
var builtinPackIDs = []string{
	"happy-path-media-buy",
	"creative-rejection",
	"cancel-pause",
	"budget-limit",
}

// BuiltinPacks lists the embedded packs with their metadata.
func BuiltinPacks() ([]PackMeta, error) {
	var out []PackMeta
	for _, id := range builtinPackIDs {
		p, err := LoadBuiltin(id)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(p.Scenarios))
		for _, s := range p.Scenarios {
			names = append(names, s.Name)
		}
		out = append(out, PackMeta{
			ID:            id,
			Name:          p.Name,
			Description:   p.Description,
			ScenarioCount: len(p.Scenarios),
			Scenarios:     names,
		})
	}
	return out, nil
}

// LoadBuiltin loads an embedded pack by id.
func LoadBuiltin(id string) (*Pack, error) {
	data, err := builtinPacksFS.ReadFile("testdata/packs/" + id + ".yaml")
	if err != nil {
		return nil, fmt.Errorf("scenarios: unknown built-in pack %q", id)
	}
	return ParsePack(data)
}

// IsBuiltinID reports whether id names an embedded pack.
func IsBuiltinID(id string) bool {
	for _, b := range builtinPackIDs {
		if b == id {
			return true
		}
	}
	return false
}

// ResolvePack loads a pack from a built-in id or a YAML file path.
// ".yaml"/".yml" suffix or a path separator forces file interpretation;
// otherwise a matching built-in id wins.
func ResolvePack(ref string) (*Pack, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("scenarios: pack reference is required")
	}
	if IsBuiltinID(ref) && !looksLikePath(ref) {
		return LoadBuiltin(ref)
	}
	abs, err := filepath.Abs(ref)
	if err != nil {
		return nil, fmt.Errorf("scenarios: resolve pack path: %w", err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("scenarios: read pack %q: %w", ref, err)
	}
	return ParsePack(data)
}

func looksLikePath(ref string) bool {
	return strings.ContainsAny(ref, `/\`) ||
		strings.HasSuffix(ref, ".yaml") || strings.HasSuffix(ref, ".yml")
}

// SortedBuiltinIDs returns the built-in pack ids in sorted order
// (for error messages).
func SortedBuiltinIDs() []string {
	out := append([]string{}, builtinPackIDs...)
	sort.Strings(out)
	return out
}
