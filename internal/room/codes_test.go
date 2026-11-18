package room

import "testing"

func TestErrorCodes(t *testing.T) {
	for _, c := range []struct {
		code    ErrorCode
		text    string
		closeAs int
		outcome PostOutcome
	}{
		{UnsupportedVersion, "unsupported_version", 4400, Rejected},
		{Unauthorized, "unauthorized", 4401, Rejected},
		{TokenExpired, "token_expired", 4401, Rejected},
		{AlreadyAuthenticated, "already_authenticated", 0, Rejected},
		{BadMessage, "bad_message", 0, Rejected},
		{Schema, "schema", 0, Rejected},
		{RightToPost, "right_to_post", 0, Rejected},
		{Forbidden, "forbidden", 0, Rejected},
		{Unsupported, "unsupported", 0, Rejected},
		{RateLimit, "rate_limit", 0, Retry},
		{Internal, "internal", 0, Retry},
		{Coalesced, "coalesced", 0, Done},
		{Behind, "behind", 0, Rejected},
		{ErrorCode("from_a_newer_hub"), "from_a_newer_hub", 0, Rejected},
		{ErrorCode(""), "", 0, Rejected},
	} {
		t.Run(c.text, func(t *testing.T) {
			if string(c.code) != c.text {
				t.Errorf("code = %q, want %q", c.code, c.text)
			}
			if got := c.code.CloseCode(); got != c.closeAs {
				t.Errorf("CloseCode = %d, want %d", got, c.closeAs)
			}
			if got := c.code.PostOutcome(); got != c.outcome {
				t.Errorf("PostOutcome = %d, want %d", got, c.outcome)
			}
		})
	}
}

func TestOnlyBehindResumes(t *testing.T) {
	for code, row := range errorCodes {
		if row.resumes != (code == Behind) {
			t.Errorf("%s: resumes = %v", code, row.resumes)
		}
	}
}
