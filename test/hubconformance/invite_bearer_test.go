package hubconformance

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/hub"
)

func (f *fixture) redeemedBearerFormat(t *testing.T) {
	for _, tc := range []struct {
		name, replacement string
		want              int
	}{
		{"comma", ",", http.StatusUnauthorized},
		{"outside-alphabet", ".", http.StatusUnauthorized},
		{"valid", "", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := newInviteBearer()
			if tc.replacement != "" {
				token = token[:21] + tc.replacement + token[22:]
			}
			memberID := "bearer-format-" + rand.Text()
			body, _ := json.Marshal(hub.InviteInput{ProjectID: *project, MemberID: memberID, Role: hub.Lead})
			invite := f.issueInvite(t, string(body))
			f.call(t, post, redeemPath, "", redeemJSON(invite.Code, memberDigest(token)), http.StatusOK)
			defer f.call(t, deleteMethod, "/api/v1/admin/members/"+memberID, f.lead, "", http.StatusOK)

			for _, path := range []string{projectPath(*project) + "/policy.bundle", eventsPath(*project), invitePath} {
				f.call(t, get, path, token, "", tc.want)
			}
		})
	}
}
