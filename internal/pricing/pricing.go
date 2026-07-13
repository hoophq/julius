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
	"math"
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
	if err := t.validate(); err != nil {
		return Builtin(), fmt.Errorf("%s: %w", path, err)
	}
	t.Source = path
	return t, nil
}

// validate rejects rate values that would make cost math lie: negative
// numbers, NaN/Inf, or a model with no positive input and output rate.
func (t Table) validate() error {
	for name, r := range t.Models {
		for field, v := range map[string]float64{
			"input": r.Input, "output": r.Output,
			"cache_read": r.CacheRead, "cache_write": r.CacheWrite,
		} {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
				return fmt.Errorf("model %q: %s = %v is not a usable rate", name, field, v)
			}
		}
		if r.Input <= 0 || r.Output <= 0 {
			return fmt.Errorf("model %q: input and output rates must be positive", name)
		}
	}
	return nil
}

// Lookup finds the rate for a model ID: exact match first, then the
// longest table key whose remaining suffix is a '-' or '@' separator
// followed by digits only — a dated snapshot of the same model
// ("claude-haiku-4-5-20251001" → "claude-haiku-4-5"). Anything else
// after a key ("gpt-5.5-mini", "gpt-5.1-codex") is a different model
// with its own price and stays unmatched: an unlisted model must
// render as unpriced, never inherit a sibling's rate.
func (t Table) Lookup(model string) (Rate, bool) {
	if r, ok := t.Models[model]; ok {
		return r, true
	}
	var best string
	var bestRate Rate
	for key, r := range t.Models {
		if len(key) > len(best) && snapshotOf(model, key) {
			best, bestRate = key, r
		}
	}
	return bestRate, best != ""
}

// snapshotOf reports whether model is key plus a dated-snapshot suffix:
// a '-' or '@' separator followed by a date-shaped tail — digits and
// dashes, starting and ending with a digit. Covers "-20251001" and
// "-2025-11-13" style snapshots; rejects named variants like "-mini".
func snapshotOf(model, key string) bool {
	if len(model) <= len(key)+1 || !strings.HasPrefix(model, key) {
		return false
	}
	switch model[len(key)] {
	case '-', '@':
	default:
		return false
	}
	tail := model[len(key)+1:]
	if !isDigit(tail[0]) || !isDigit(tail[len(tail)-1]) {
		return false
	}
	for i := 0; i < len(tail); i++ {
		if !isDigit(tail[i]) && tail[i] != '-' {
			return false
		}
	}
	return true
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

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

// CacheNet is the net cost effect of caching: what the cache reads
// saved against the full input rate, minus the premium paid on cache
// writes above that rate. Positive means caching saved money in this
// window; negative means write premiums outweighed read savings.
func (r Rate) CacheNet(cacheRead, cacheWrite int) float64 {
	const mtok = 1e6
	var net float64
	if d := r.Input - r.CacheRead; d > 0 && cacheRead > 0 {
		net += float64(cacheRead) / mtok * d
	}
	if p := r.CacheWrite - r.Input; p > 0 && cacheWrite > 0 {
		net -= float64(cacheWrite) / mtok * p
	}
	return net
}
