// Package pricing turns exact provider-reported token counts into USD
// estimates via a dated, override-able per-model rate table.
//
// Cost applies only to the exact API-usage surface. The hook and
// compression surfaces are token estimates, and pricing an estimate
// would present a made-up number as money. The rate table is the one
// estimated input here — prices change over time — so every figure it
// produces is labeled with the table's as-of date.
package pricing

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Rate is USD per million tokens for one model.
type Rate struct {
	Input      float64 `toml:"input"`
	Output     float64 `toml:"output"`
	CacheRead  float64 `toml:"cache_read"`
	CacheWrite float64 `toml:"cache_write"`
}

// Table is a dated set of per-model rates.
type Table struct {
	AsOf   string          `toml:"as_of"`
	Models map[string]Rate `toml:"models"`
	Source string          `toml:"-"` // "builtin" or the override file path
}

//go:embed default_pricing.toml
var builtinTOML []byte

// Builtin returns the embedded default table.
func Builtin() Table {
	var t Table
	if err := toml.Unmarshal(builtinTOML, &t); err != nil {
		// the embedded table is validated by tests; reaching this means
		// a broken build, not a runtime condition
		panic(fmt.Sprintf("pricing: embedded table invalid: %v", err))
	}
	t.Source = "builtin"
	return t
}

// OverridePath returns where a user pricing table is looked for:
// JULIUS_PRICING if set, else <user config dir>/julius/pricing.toml.
func OverridePath() string {
	if p := os.Getenv("JULIUS_PRICING"); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "julius", "pricing.toml")
}

// Load returns the active table. An override file replaces the builtin
// table entirely — no merging, so the provenance of every rate is
// unambiguous. A present-but-unusable override returns the builtin
// table alongside the error: callers warn, cost still renders.
func Load() (Table, error) {
	path := OverridePath()
	if path == "" {
		return Builtin(), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Builtin(), nil
		}
		return Builtin(), err
	}
	var t Table
	if err := toml.Unmarshal(data, &t); err != nil {
		return Builtin(), fmt.Errorf("%s: %w", path, err)
	}
	if t.AsOf == "" {
		return Builtin(), fmt.Errorf("%s: missing as_of date", path)
	}
	t.Source = path
	return t, nil
}

// Lookup finds the rate for a model ID: exact match first, then the
// longest table key that is a prefix ending at a separator ('-', '@',
// ':', '_'). Dated or platform-suffixed variants match their base entry
// ("claude-haiku-4-5-20251001" → "claude-haiku-4-5") while a key can
// never claim a differently-priced sibling ("gpt-5" vs "gpt-5.1").
func (t Table) Lookup(model string) (Rate, bool) {
	if r, ok := t.Models[model]; ok {
		return r, true
	}
	var best string
	var bestRate Rate
	for key, r := range t.Models {
		if len(key) >= len(model) || !strings.HasPrefix(model, key) {
			continue
		}
		switch model[len(key)] {
		case '-', '@', ':', '_':
		default:
			continue
		}
		if len(key) > len(best) {
			best, bestRate = key, r
		}
	}
	return bestRate, best != ""
}

// Cost prices one usage aggregate, in USD. Providers report input
// differently — Anthropic's input_tokens exclude cache reads and
// writes, OpenAI's prompt_tokens include cached tokens — and the ledger
// stores the provider's numbers verbatim, so the normalization lives
// here.
func (r Rate) Cost(provider string, input, output, cacheRead, cacheWrite int) float64 {
	in := input
	if provider == "openai" {
		in -= cacheRead
		if in < 0 {
			in = 0
		}
	}
	const mtok = 1e6
	return float64(in)/mtok*r.Input +
		float64(output)/mtok*r.Output +
		float64(cacheRead)/mtok*r.CacheRead +
		float64(cacheWrite)/mtok*r.CacheWrite
}

// CacheAvoided is what the cache reads would have cost at the full
// input rate minus what they did cost: cost avoided by caching.
func (r Rate) CacheAvoided(cacheRead int) float64 {
	diff := r.Input - r.CacheRead
	if diff <= 0 || cacheRead <= 0 {
		return 0
	}
	return float64(cacheRead) / 1e6 * diff
}
