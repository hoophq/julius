package filter

import (
	_ "embed"
	"sort"
	"testing"

	"github.com/BurntSushi/toml"
)

//go:embed catalog.toml
var catalogTOML []byte

// catalogRow is one target command in catalog.toml. A row is either covered
// (Filter set) or a tracked gap (Status "planned" with Ref, or "wont-cover"
// with Reason).
type catalogRow struct {
	Cmd      string `toml:"cmd"`
	Category string `toml:"category"`
	Filter   string `toml:"filter"`
	Status   string `toml:"status"`
	Ref      string `toml:"ref"`
	Reason   string `toml:"reason"`
}

// TestCatalogCoverage keeps catalog.toml honest in both directions: covered
// rows must resolve to exactly the filter they claim, gap rows must not
// resolve at all (shipping a filter forces the row flip), and every builtin
// filter must appear in at least one covered row.
func TestCatalogCoverage(t *testing.T) {
	var cat struct {
		Commands []catalogRow `toml:"commands"`
	}
	if err := toml.Unmarshal(catalogTOML, &cat); err != nil {
		t.Fatalf("catalog.toml: %v", err)
	}
	if len(cat.Commands) == 0 {
		t.Fatal("catalog.toml: no commands")
	}

	// Builtins only: a project or user filter tier on the machine running
	// the tests must not shadow what the catalog claims about builtins.
	reg := &Registry{builtin: mustBuiltin()}

	type tally struct{ covered, planned int }
	perCat := map[string]*tally{}
	seen := map[string]bool{}
	usedFilter := map[string]bool{}

	for _, row := range cat.Commands {
		if row.Cmd == "" || row.Category == "" {
			t.Errorf("row %+v: cmd and category are required", row)
			continue
		}
		if seen[row.Cmd] {
			t.Errorf("%q: duplicate row", row.Cmd)
			continue
		}
		seen[row.Cmd] = true
		if perCat[row.Category] == nil {
			perCat[row.Category] = &tally{}
		}

		status := row.Status
		if status == "" {
			status = "covered"
		}
		got := reg.Pick(row.Cmd)

		switch status {
		case "covered":
			if row.Filter == "" {
				t.Errorf("%q: covered row must name its filter", row.Cmd)
				continue
			}
			switch {
			case got == nil:
				t.Errorf("%q: claims filter %q, but no builtin matches", row.Cmd, row.Filter)
			case got.Name() != row.Filter:
				t.Errorf("%q: claims filter %q, but Pick resolves to %q", row.Cmd, row.Filter, got.Name())
			default:
				usedFilter[row.Filter] = true
				perCat[row.Category].covered++
			}
		case "planned":
			if row.Ref == "" {
				t.Errorf("%q: planned row must carry a ref", row.Cmd)
			}
			if got != nil {
				t.Errorf("%q: marked planned, but builtin %q already matches — flip the row to covered", row.Cmd, got.Name())
			}
			perCat[row.Category].planned++
		case "wont-cover":
			if row.Reason == "" {
				t.Errorf("%q: wont-cover row must carry a reason", row.Cmd)
			}
			if got != nil {
				t.Errorf("%q: marked wont-cover, but builtin %q matches — flip the row to covered", row.Cmd, got.Name())
			}
		default:
			t.Errorf("%q: unknown status %q", row.Cmd, status)
		}
	}

	for _, s := range reg.Builtin() {
		if !usedFilter[s.Name()] {
			t.Errorf("builtin filter %q has no covered row in catalog.toml — add one", s.Name())
		}
	}

	cats := make([]string, 0, len(perCat))
	for c := range perCat {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	var covered, planned int
	for _, c := range cats {
		s := perCat[c]
		covered += s.covered
		planned += s.planned
		t.Logf("%-12s %3d/%-3d covered  %5.1f%%", c, s.covered, s.covered+s.planned, pct(s.covered, s.covered+s.planned))
	}
	t.Logf("catalog coverage: %d/%d commands  %.1f%%", covered, covered+planned, pct(covered, covered+planned))
}

func pct(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return float64(part) / float64(whole) * 100
}
