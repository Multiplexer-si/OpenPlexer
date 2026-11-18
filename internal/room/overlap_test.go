package room

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// spec/room/README.md, open question 7: the cases that version 1 does not
// define, where a claim must fail closed.
func TestAGlobThatIsNotReadOverlapsEveryEntry(t *testing.T) {
	for name, glob := range map[string]string{
		"a name outside ASCII":                          "[]|(?<é>a)]",
		"a group with flags":                            "[]|(?i:a)]",
		"a group with only (?":                          "[]|(?)]",
		"more than 1000 groups":                         "[]|" + strings.Repeat("()", 1001) + "]",
		"a glob of 2048 stars":                          strings.Repeat("*a", 2048),
		"a large count of a group that can match empty": "[]|(?:a?){4000000000}]",
		"a count inside a count":                        "[]|(?:(?:x?){2000}$|x){2000}y]",
		"a large count inside a lookahead":              "[]|(?=(?:a*){10000000})]",
	} {
		t.Run(name, func(t *testing.T) {
			for _, other := range []string{"docs/guide.md", "docs/**"} {
				if !PatternsOverlap(glob, other) || !PatternsOverlap(other, glob) {
					t.Errorf("the glob does not overlap %s", other)
				}
			}
		})
	}
}

func TestAPathThatIsTooLongForTheGlobOverlapsIt(t *testing.T) {
	count := longestPath + 400
	glob := fmt.Sprintf("[]|^a{0,%d}]", count)
	if PatternsOverlap(glob, "b"+strings.Repeat("a", longestPath-2)+"]") {
		t.Error("the glob overlaps a path of 4,096 characters that it does not match")
	}
	if !PatternsOverlap(glob, "b"+strings.Repeat("a", count)+"]") {
		t.Error("the glob does not overlap a path that is too long to match")
	}
}

func TestOverlapAnswersInTime(t *testing.T) {
	long := strings.Repeat("ab/", 1333) + "c]"
	for name, c := range map[string]struct {
		glob, path string
		want       bool
	}{
		"a lookbehind":                             {"[]|(?<=b)]", strings.Repeat("a", 4000) + "b", false},
		"a negative lookbehind":                    {"[]|(?<!x)c]", long, true},
		"twelve lookbehinds, each inside the next": {"[]|" + strings.Repeat("(?<=", 12) + "a" + strings.Repeat(")", 12) + "]", strings.Repeat("a", 4095) + "]", true},
		"a repeated group inside a repeated group": {"[]|^(?:(?:x?)*$|x)*y]", strings.Repeat("x", 4096), false},
		"a glob of 700 stars":                      {strings.Repeat("*a", 700) + "c", strings.Repeat("a", 4096), false},
		"a glob of 400 directory runs":             {strings.Repeat("**/", 400) + "c", strings.Repeat("a/", 2048), false},
	} {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			if got := PatternsOverlap(c.glob, c.path); got != c.want {
				t.Errorf("PatternsOverlap = %v, want %v", got, c.want)
			}
			if took := time.Since(start); took > time.Second {
				t.Errorf("the answer took %v", took)
			}
		})
	}
}
