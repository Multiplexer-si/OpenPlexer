package room

import "testing"

func TestErrorCodes(t *testing.T) {
	for _, c := range []struct {
		code    ErrorCode
		text    string
		outcome PostOutcome
	}{
		{UnsupportedVersion, "unsupported_version", Rejected},
		{Unauthorized, "unauthorized", Rejected},
		{TokenExpired, "token_expired", Rejected},
		{MpxOutdated, "mpx_outdated", Rejected},
		{AlreadyAuthenticated, "already_authenticated", Rejected},
		{BadMessage, "bad_message", Rejected},
		{Schema, "schema", Rejected},
		{RightToPost, "right_to_post", Rejected},
		{Forbidden, "forbidden", Rejected},
		{Unsupported, "unsupported", Rejected},
		{RateLimit, "rate_limit", Retry},
		{Internal, "internal", Retry},
		{Coalesced, "coalesced", Done},
		{Behind, "behind", Rejected},
		{ErrorCode("from_a_newer_hub"), "from_a_newer_hub", Rejected},
		{ErrorCode(""), "", Rejected},
	} {
		t.Run(c.text, func(t *testing.T) {
			if string(c.code) != c.text {
				t.Errorf("code = %q, want %q", c.code, c.text)
			}
			if got := c.code.PostOutcome(); got != c.outcome {
				t.Errorf("PostOutcome = %d, want %d", got, c.outcome)
			}
		})
	}
}

func TestACodeThatTheTableDoesNotListKeepsTheConnection(t *testing.T) {
	for _, code := range []ErrorCode{"from_a_newer_hub", ""} {
		if got := code.CloseCode(); got != 0 {
			t.Errorf("%q: CloseCode = %d, want 0", code, got)
		}
	}
}

func TestOnlyBehindResumes(t *testing.T) {
	for code, row := range errorCodes {
		if row.resumes != (code == Behind) {
			t.Errorf("%s: resumes = %v", code, row.resumes)
		}
	}
}
