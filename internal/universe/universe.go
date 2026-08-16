// Package universe loads the tradable symbol universe from the trader-owned
// basket config (docs/foundations/morning-tape-baskets-v2.json). The JSON is
// the machine-readable source of truth; this package only reads it — basket
// edits are the PM's, never code (D1).
package universe

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// basketsFile mirrors only the parts of the config this package needs.
// Unknown fields (settings, notes, benchmark assignments) pass through
// untouched so trader edits never break loading.
type basketsFile struct {
	Benchmarks map[string][]string `json:"benchmarks"`
	Baskets    map[string]struct {
		Members []string `json:"members"`
	} `json:"baskets"`
}

// Basket is one trader-defined basket: its config key and its members,
// full membership (display/tiles/glow/breadth keep full membership; only
// 3.7's cross-basket narrative computes on disjoint sets).
type Basket struct {
	Name    string
	Members []string
}

// LoadBaskets returns the baskets sorted by name, members sorted within
// each — the deterministic row order the 3.0 dev view renders.
func LoadBaskets(path string) ([]Basket, error) {
	cfg, err := read(path)
	if err != nil {
		return nil, err
	}
	if len(cfg.Baskets) == 0 {
		return nil, fmt.Errorf("baskets config %s has no baskets", path)
	}
	out := make([]Basket, 0, len(cfg.Baskets))
	for name, b := range cfg.Baskets {
		if len(b.Members) == 0 {
			return nil, fmt.Errorf("baskets config %s: basket %q has no members", path, name)
		}
		members := append([]string(nil), b.Members...)
		sort.Strings(members)
		// De-duplicate like Load does: the config is trader-edited, and a
		// repeated member must not double-count in basket sums (full config
		// validation is backlogged — universe loader hardening).
		uniq := members[:0]
		for i, m := range members {
			if i == 0 || m != members[i-1] {
				uniq = append(uniq, m)
			}
		}
		out = append(out, Basket{Name: name, Members: uniq})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func read(path string) (*basketsFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read baskets config: %w", err)
	}
	var cfg basketsFile
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse baskets config %s: %w", path, err)
	}
	return &cfg, nil
}

// Load returns the sorted, de-duplicated equity universe: all basket members
// plus all benchmarks, excluding the bond_gate_futures group (ZN/ZB are a
// separate futures dataset, backlogged — see docs/foundations/data.md).
func Load(path string) ([]string, error) {
	cfg, err := read(path)
	if err != nil {
		return nil, err
	}
	set := map[string]struct{}{}
	for group, syms := range cfg.Benchmarks {
		// ⚠ Fragile by necessity: this trader-owned group NAME is the only
		// key excluding non-equity symbols, and the trader may rename or
		// split groups at will (the config is theirs — we must not edit it).
		// The robust fix is a schema field on the group (e.g. equity: false)
		// filtered on as data — proposed to the trader via docs/backlog.md.
		// Until then: a rename here silently changes the universe, and a
		// futures symbol placed in a basket's members would leak through
		// unconditionally (members have no exclusion path at all).
		if group == "bond_gate_futures" {
			continue
		}
		for _, s := range syms {
			set[s] = struct{}{}
		}
	}
	for _, b := range cfg.Baskets {
		for _, s := range b.Members {
			set[s] = struct{}{}
		}
	}
	if len(set) == 0 {
		return nil, fmt.Errorf("baskets config %s yielded an empty universe", path)
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, nil
}
