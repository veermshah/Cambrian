package main

import (
	"errors"
	"fmt"

	yaml "github.com/goccy/go-yaml"
)

// seedFile is the top-level YAML shape.
type seedFile struct {
	Nodes []SeedSpec `yaml:"nodes"`
}

// ParseSeedYAML decodes the file contents into a slice of SeedSpec. The
// parser does shape validation only — semantic checks (chain, task type,
// node class) run later in SeedSpec.Validate so the error messages can
// name the specific row.
func ParseSeedYAML(raw []byte) ([]SeedSpec, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty seed file")
	}
	var f seedFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	if len(f.Nodes) == 0 {
		return nil, errors.New("seed file: nodes list is empty")
	}
	// Catch duplicate names early — the agents.name column doesn't have
	// a UNIQUE constraint, but two rows with the same name would make
	// the per-name idempotency check ambiguous.
	seen := map[string]struct{}{}
	for i, n := range f.Nodes {
		if n.Name == "" {
			return nil, fmt.Errorf("nodes[%d]: name required", i)
		}
		if _, dup := seen[n.Name]; dup {
			return nil, fmt.Errorf("nodes[%d]: duplicate name %q", i, n.Name)
		}
		seen[n.Name] = struct{}{}
	}
	return f.Nodes, nil
}
