package vision

import (
	"testing"

	"tender/api/internal/money"
)

func TestParseClaudeJSONToleratesAFence(t *testing.T) {
	got, err := parseClaudeJSON("```json\n{\"counts\":{\"1000\":2},\"confidence\":0.9}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if got.Counts["1000"] != 2 {
		t.Errorf("counts = %v, want 2 × ₦1000", got.Counts)
	}
}

func TestParseClaudeJSONRejectsGarbage(t *testing.T) {
	if _, err := parseClaudeJSON("I could not read the image, sorry."); err == nil {
		t.Error("expected an error for a non-JSON reply")
	}
}

func TestExpandProducesOneNotePerCount(t *testing.T) {
	notes, _ := expand(&claudeResponse{
		Counts: map[string]int{"1000": 3, "500": 2},
	}, "abcdef0123456789", nil)

	if len(notes) != 5 {
		t.Fatalf("expanded %d notes, want 5", len(notes))
	}

	var total money.Kobo
	for _, n := range notes {
		total += n.Denomination
	}
	if want := money.FromNaira(4000); total != want {
		t.Errorf("total = %s, want %s", total, want)
	}

	// Largest denomination first, so the notes that carry serials are the ones
	// a replay would be most profitable on.
	if notes[0].Denomination != money.FromNaira(1000) {
		t.Errorf("first note is %s, want ₦1,000", notes[0].Denomination)
	}
}

func TestExpandGivesEveryNoteADistinctIdentity(t *testing.T) {
	notes, _ := expand(&claudeResponse{Counts: map[string]int{"500": 6}}, "abcdef0123456789", nil)

	seen := map[string]bool{}
	for _, n := range notes {
		if n.PHash == "" {
			t.Fatal("a note without a perceptual hash cannot be registered")
		}
		if seen[n.PHash] {
			t.Fatalf("duplicate note identity %q would collide in the registry", n.PHash)
		}
		seen[n.PHash] = true
	}
}

func TestExpandAttachesSerialsToTheLargestNotes(t *testing.T) {
	notes, _ := expand(&claudeResponse{
		Counts:  map[string]int{"1000": 2, "100": 3},
		Serials: []string{"ab/12 3948217", "  CD/34 1122334  "},
	}, "abcdef0123456789", nil)

	if notes[0].Serial != "AB/12 3948217" {
		t.Errorf("serial not normalised: %q", notes[0].Serial)
	}
	if notes[1].Serial != "CD/34 1122334" {
		t.Errorf("whitespace not collapsed: %q", notes[1].Serial)
	}
	// Only two serials were legible, so the rest must fall back to the hash.
	for _, n := range notes[2:] {
		if n.Serial != "" {
			t.Errorf("invented a serial for a note that had none: %q", n.Serial)
		}
	}
}

func TestExpandDropsSerialDuplicates(t *testing.T) {
	notes, _ := expand(&claudeResponse{
		Counts:  map[string]int{"1000": 3},
		Serials: []string{"AB/12 3948217", "ab/12 3948217", "CD/34 1122334"},
	}, "abcdef0123456789", nil)

	if notes[0].Serial != "AB/12 3948217" || notes[1].Serial != "CD/34 1122334" {
		t.Errorf("duplicate serial not collapsed: %q, %q", notes[0].Serial, notes[1].Serial)
	}
}

// A count is only trustworthy if impossible denominations are thrown away
// rather than quietly added to the total.
func TestExpandRejectsImpossibleDenominations(t *testing.T) {
	notes, warnings := expand(&claudeResponse{
		Counts: map[string]int{"1000": 1, "300": 4},
	}, "abcdef0123456789", nil)

	if len(notes) != 1 {
		t.Errorf("expanded %d notes, want only the real ₦1,000", len(notes))
	}
	if len(warnings) == 0 {
		t.Error("dropping a fake denomination must be reported, not silent")
	}
}

func TestExpandIgnoresZeroAndNegativeCounts(t *testing.T) {
	notes, _ := expand(&claudeResponse{
		Counts: map[string]int{"1000": 0, "500": -2, "100": 1},
	}, "abcdef0123456789", nil)

	if len(notes) != 1 || notes[0].Denomination != money.FromNaira(100) {
		t.Errorf("got %d notes, want a single ₦100", len(notes))
	}
}
