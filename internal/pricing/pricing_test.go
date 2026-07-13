package pricing

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func approx(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuiltinTableParses(t *testing.T) {
	tbl := Builtin()
	if tbl.AsOf == "" {
		t.Fatal("builtin table has no as_of date")
	}
	if tbl.Source != "builtin" {
		t.Fatalf("source = %q", tbl.Source)
	}
	r, ok := tbl.Lookup("claude-opus-4-8")
	if !ok {
		t.Fatal("claude-opus-4-8 missing from builtin table")
	}
	if r.Input <= 0 || r.Output <= 0 || r.CacheRead <= 0 || r.CacheWrite <= 0 {
		t.Fatalf("claude-opus-4-8 has zero rates: %+v", r)
	}
	for name, r := range tbl.Models {
		if r.Input <= 0 || r.Output <= 0 {
			t.Errorf("%s: input/output must be positive: %+v", name, r)
		}
	}
}

func TestLookupPrefix(t *testing.T) {
	tbl := Table{Models: map[string]Rate{
		"claude-haiku-4-5": {Input: 1},
		"gpt-5":            {Input: 99},
		"gpt-5.4":          {Input: 2.5},
		"gpt-5.4-mini":     {Input: 0.75},
	}}

	// dated snapshot matches the base entry across a '-' boundary
	r, ok := tbl.Lookup("claude-haiku-4-5-20251001")
	if !ok || r.Input != 1 {
		t.Fatalf("dated variant: ok=%v r=%+v", ok, r)
	}
	// vertex-style '@' boundary
	if _, ok := tbl.Lookup("claude-haiku-4-5@20251001"); !ok {
		t.Fatal("@-suffixed variant did not match")
	}
	// dashed date snapshots match too
	r, _ = tbl.Lookup("gpt-5.4-mini-2026-01-01")
	if r.Input != 0.75 {
		t.Fatalf("dashed date snapshot: got %+v", r)
	}
	// a key never claims a differently-priced sibling across '.'
	if _, ok := tbl.Lookup("gpt-5.1"); ok {
		t.Fatal("gpt-5 must not match gpt-5.1")
	}
	// a NAMED variant is a different model with its own price — it must
	// render as unpriced, never inherit the base entry's rate
	for _, m := range []string{"gpt-5.4-turbo", "gpt-5.4-mini-hd", "claude-haiku-4-5-mini", "gpt-5.4-20260101x"} {
		if _, ok := tbl.Lookup(m); ok {
			t.Errorf("%s must not inherit a sibling's rate", m)
		}
	}
	// exact beats prefix
	r, _ = tbl.Lookup("gpt-5.4")
	if r.Input != 2.5 {
		t.Fatalf("exact: got %+v", r)
	}
	if _, ok := tbl.Lookup("unknown-model"); ok {
		t.Fatal("unknown model matched")
	}
}

func TestCostProviderSemantics(t *testing.T) {
	r := Rate{Input: 10, Output: 50, CacheRead: 1, CacheWrite: 12.5}

	// anthropic: input_tokens exclude cache reads/writes — all add up
	approx(t, r.Cost("anthropic", 1_000_000, 100_000, 500_000, 200_000),
		10+5+0.5+2.5)

	// openai: prompt_tokens include cached tokens — cached part is
	// billed at cache_read, not double-counted at the input rate
	approx(t, r.Cost("openai", 1_000_000, 100_000, 400_000, 0),
		6+5+0.4)

	// pathological: cache read larger than reported input never goes negative
	if c := r.Cost("openai", 100, 0, 200, 0); c < 0 {
		t.Fatalf("negative cost: %v", c)
	}
}

func TestCacheNet(t *testing.T) {
	r := Rate{Input: 10, CacheRead: 1, CacheWrite: 12.5}
	// reads only: full read-side saving
	approx(t, r.CacheNet(1_000_000, 0), 9)
	// writes only: the premium above the input rate is a net cost
	approx(t, r.CacheNet(0, 1_000_000), -2.5)
	// both: saving minus premium
	approx(t, r.CacheNet(1_000_000, 1_000_000), 6.5)
	approx(t, r.CacheNet(0, 0), 0)
	// no read discount, no write premium → nothing to report
	approx(t, Rate{Input: 1, CacheRead: 1, CacheWrite: 1}.CacheNet(1_000_000, 1_000_000), 0)
}

func TestLoadOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pricing.toml")
	body := `
as_of = "2030-01-01"
[models."my-model"]
input = 1.0
output = 2.0
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JULIUS_PRICING", path)

	tbl, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if tbl.Source != path || tbl.AsOf != "2030-01-01" {
		t.Fatalf("source=%q asof=%q", tbl.Source, tbl.AsOf)
	}
	// replacement, not merge
	if _, ok := tbl.Lookup("claude-opus-4-8"); ok {
		t.Fatal("override table must fully replace the builtin one")
	}
	if _, ok := tbl.Lookup("my-model"); !ok {
		t.Fatal("override model missing")
	}
}

func TestLoadOverrideMissing(t *testing.T) {
	t.Setenv("JULIUS_PRICING", filepath.Join(t.TempDir(), "nope.toml"))
	tbl, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if tbl.Source != "builtin" {
		t.Fatalf("source = %q", tbl.Source)
	}
}

func TestLoadOverrideInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pricing.toml")
	if err := os.WriteFile(path, []byte("not toml {{"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JULIUS_PRICING", path)

	tbl, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid override")
	}
	if tbl.Source != "builtin" {
		t.Fatal("invalid override must fall back to builtin")
	}
}

func TestLoadOverrideBadRates(t *testing.T) {
	for name, body := range map[string]string{
		"negative": "as_of = \"2030-01-01\"\n[models.\"m\"]\ninput = -3.0\noutput = 1.0\n",
		"nan":      "as_of = \"2030-01-01\"\n[models.\"m\"]\ninput = 1.0\noutput = nan\n",
		"inf":      "as_of = \"2030-01-01\"\n[models.\"m\"]\ninput = inf\noutput = 1.0\n",
		"zero":     "as_of = \"2030-01-01\"\n[models.\"m\"]\ninput = 0.0\noutput = 1.0\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pricing.toml")
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("JULIUS_PRICING", path)
			tbl, err := Load()
			if err == nil {
				t.Fatal("expected validation error")
			}
			if tbl.Source != "builtin" {
				t.Fatal("bad rates must fall back to builtin")
			}
		})
	}
}

func TestLoadOverrideMissingAsOf(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pricing.toml")
	body := "[models.\"m\"]\ninput = 1.0\noutput = 1.0\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JULIUS_PRICING", path)

	if _, err := Load(); err == nil {
		t.Fatal("expected error for override without as_of")
	}
}
