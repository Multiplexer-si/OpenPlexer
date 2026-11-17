package policy

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TimestampPattern is the pattern of `now` in eval-query.schema.json and of
// `expires_at` in a temporary grant.
const TimestampPattern = `^(?:(?:[0-9][0-9][2468][048]|[0-9][0-9][13579][26]|[0-9][0-9]0[48]|[02468][048]00|[13579][26]00)-02-29|[0-9]{4}-(?:(?:0[13578]|1[02])-(?:0[1-9]|[12][0-9]|3[01])|(?:0[469]|11)-(?:0[1-9]|[12][0-9]|30)|(?:02)-(?:0[1-9]|1[0-9]|2[0-8])))T(?:(?:[01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9](?:\.[0-9]+)?(?:Z|([+-](?:[01][0-9]|2[0-3]):[0-5][0-9])))$`

var timestampForm = regexp.MustCompile(TimestampPattern)

// Timestamp is an instant in whole milliseconds. The zero value is no time:
// it is before nothing and nothing is before it.
type Timestamp struct {
	ms  int64
	set bool
}

func TimestampOf(t time.Time) Timestamp {
	return Timestamp{ms: t.UnixMilli(), set: true}
}

// ParseTimestamp reads a timestamp in the form of the policy contract. It
// drops the digits of the fraction after the third and does not round.
func ParseTimestamp(text string) (Timestamp, bool) {
	if !timestampForm.MatchString(text) {
		return Timestamp{}, false
	}
	field := func(from, to int) int {
		n, _ := strconv.Atoi(text[from:to])
		return n
	}
	seconds := time.Date(field(0, 4), time.Month(field(5, 7)), field(8, 10), field(11, 13), field(14, 16), field(17, 19), 0, time.UTC)
	ms := seconds.UnixMilli()

	rest := text[19:]
	if rest[0] == '.' {
		end := strings.IndexAny(rest, "Z+-")
		for i, scale := 1, int64(100); i < end && i <= 3; i, scale = i+1, scale/10 {
			ms += int64(rest[i]-'0') * scale
		}
		rest = rest[end:]
	}
	if rest != "Z" {
		offset := int64(field(len(text)-5, len(text)-3)*60+field(len(text)-2, len(text))) * 60_000
		if rest[0] == '+' {
			ms -= offset
		} else {
			ms += offset
		}
	}
	return Timestamp{ms: ms, set: true}, true
}

func (t Timestamp) Before(u Timestamp) bool {
	return t.set && u.set && t.ms < u.ms
}
