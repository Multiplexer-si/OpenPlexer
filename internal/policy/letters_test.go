package policy

import (
	"testing"
	"unicode"
)

// A class finds the other cases of a letter among the letters that
// unicode.SimpleFold returns for it. That search is complete only if every
// unit shares a fold orbit with its canonical form, and the canonical form
// is canonical itself. A new version of the Unicode tables of Go that breaks
// either one fails here.
func TestUnitsWithOneCanonicalFormFoldTogether(t *testing.T) {
	for u := range 0x10000 {
		unit := uint16(u)
		form := canonical(unit)
		if canonical(form) != form {
			t.Errorf("canonical(%#04x) = %#04x, whose canonical form is %#04x", unit, form, canonical(form))
		}
		if form == unit {
			continue
		}
		found := false
		for other := unicode.SimpleFold(rune(unit)); other != rune(unit); other = unicode.SimpleFold(other) {
			found = found || other == rune(form)
		}
		if !found {
			t.Errorf("%#04x and its canonical form %#04x do not fold together", unit, form)
		}
	}
}
