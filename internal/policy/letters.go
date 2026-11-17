package policy

import (
	"strings"
	"unicode"
	"unicode/utf16"
)

// The policy contract compares without case as the reference does, with two
// rules of ECMA-262. This file holds both. See the sixth open question of
// spec/policy/README.md.

// canonical is Canonicalize of ECMA-262 for a regular expression with the i
// flag and without the u flag. Two UTF-16 code units are the same letter in
// a glob if their canonical forms are equal.
//
// The form is the uppercase of the unit, with three exceptions that keep the
// unit as it is: the uppercase is more than one unit, the uppercase is ASCII
// and the unit is not, or the unit is a surrogate.
func canonical(unit uint16) uint16 {
	if unit < 0x80 {
		if 'a' <= unit && unit <= 'z' {
			return unit - 'a' + 'A'
		}
		return unit
	}
	r := rune(unit)
	if utf16.IsSurrogate(r) || uppercaseIsLonger(r) {
		return unit
	}
	upper := unicode.ToUpper(r)
	if upper < 0x80 || upper > 0xFFFF {
		return unit
	}
	return uint16(upper)
}

// uppercaseIsLonger reports the letters for which unicode.ToUpper gives one
// letter and the full case mapping of Unicode gives two: the Greek letters
// with an iota subscript, such as U+1F80, whose uppercase is U+1F08 U+0399.
// Every other letter with a longer uppercase, such as U+00DF, has no
// uppercase in unicode.ToUpper.
func uppercaseIsLonger(r rune) bool {
	switch {
	case 0x1F80 <= r && r <= 0x1FAF:
		return r&0x8 == 0
	case r == 0x1FB3, r == 0x1FC3, r == 0x1FF3:
		return true
	}
	return false
}

// lowercase is String.prototype.toLowerCase of ECMA-262: the full lowercase
// mapping of Unicode, without the rules of a language.
func lowercase(text string) string {
	runes := []rune(text)
	var out strings.Builder
	for i, r := range runes {
		switch {
		case r == 'İ':
			out.WriteString("i̇")
		case r == 'Σ' && endsWord(runes, i):
			out.WriteRune('ς')
		default:
			out.WriteRune(unicode.ToLower(r))
		}
	}
	return out.String()
}

// endsWord is the Final_Sigma condition of Unicode for the sigma at i: a
// cased letter is before it and no cased letter is after it, where each
// search skips the characters that case ignores.
func endsWord(runes []rune, i int) bool {
	before := i - 1
	for before >= 0 && caseIgnores(runes[before]) {
		before--
	}
	after := i + 1
	for after < len(runes) && caseIgnores(runes[after]) {
		after++
	}
	return before >= 0 && cased(runes[before]) && !(after < len(runes) && cased(runes[after]))
}

func cased(r rune) bool {
	return unicode.In(r, unicode.Lu, unicode.Ll, unicode.Lt, unicode.Other_Lowercase, unicode.Other_Uppercase)
}

// caseIgnores is the Case_Ignorable property of Unicode: the general
// categories Mn, Me, Cf, Lm, and Sk, and the characters whose Word_Break is
// MidLetter, MidNumLet, or Single_Quote.
func caseIgnores(r rune) bool {
	if unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk) {
		return true
	}
	switch r {
	case '\'', '.', ':', 0x00B7, 0x0387, 0x055F, 0x05F4, 0x2018, 0x2019, 0x2024, 0x2027, 0xFE13, 0xFE52, 0xFE55, 0xFF07, 0xFF0E, 0xFF1A:
		return true
	}
	return false
}

func utf16Length(text string) int {
	n := 0
	for _, r := range text {
		n += utf16.RuneLen(r)
	}
	return n
}
