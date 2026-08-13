package universe

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "baskets.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	path := write(t, `{
		"version": 2,
		"settings": {"ignored": true},
		"benchmarks": {
			"broad": ["SPY", "QQQ"],
			"bond_gate_futures": ["ZN", "ZB"],
			"cash_proxy": ["BIL"]
		},
		"baskets": {
			"a": {"members": ["NVDA", "MSFT"], "benchmark": "QQQ"},
			"b": {"members": ["NVDA", "AAPL"], "benchmark": "SPY"}
		}
	}`)
	syms, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// Deduped (NVDA in two baskets), sorted, ZN/ZB excluded.
	want := []string{"AAPL", "BIL", "MSFT", "NVDA", "QQQ", "SPY"}
	if len(syms) != len(want) {
		t.Fatalf("got %v, want %v", syms, want)
	}
	for i, s := range want {
		if syms[i] != s {
			t.Fatalf("got %v, want %v (sorted, deduped, futures excluded)", syms, want)
		}
	}
}

func TestLoadEmptyUniverseIsError(t *testing.T) {
	path := write(t, `{"benchmarks": {"bond_gate_futures": ["ZN"]}, "baskets": {}}`)
	if _, err := Load(path); err == nil {
		t.Fatal("empty universe must be an error, not a silent zero-symbol run")
	}
}

func TestLoadMissingFileAndBadJSON(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("missing file must error")
	}
	if _, err := Load(write(t, `{not json`)); err == nil {
		t.Fatal("malformed JSON must error")
	}
}
