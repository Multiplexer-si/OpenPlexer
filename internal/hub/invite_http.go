package hub

import (
	"bytes"
	"crypto/sha256"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const inviteBodyLimit = 64 << 10

func (s *Store) inviteRoutes(mux *http.ServeMux) {
	const admin = "/api/v1/admin/"
	mux.HandleFunc(admin+"invites", s.inviteEndpoint("GET, HEAD, POST", true, func(st *state, req request) (int, any, error) {
		if req.Method == http.MethodPost {
			input := InviteInput{Role: Member, ExpiresIn: DefaultInviteExpiry}
			if err := decodeInviteBody(req.body, &input); err != nil {
				return 0, nil, err
			}
			result, err := st.createInvite(input, &req.member, s.inviteNow())
			if err == nil {
				err = s.save(st)
			}
			return http.StatusCreated, result, err
		}
		if len(req.body) != 0 {
			return 0, nil, errInvalidInvite
		}
		after, limit, err := invitePageQuery(req.URL.RawQuery)
		if err != nil {
			return 0, nil, err
		}
		page, err := s.listInvites(st, after, limit)
		return http.StatusOK, page, err
	}))
	for _, route := range []struct {
		path, method string
		to           inviteState
	}{
		{admin + "invites/{invite}", http.MethodDelete, revoked},
		{admin + "invites/{invite}/approve", http.MethodPost, approved},
	} {
		mux.HandleFunc(route.path, s.inviteEndpoint(route.method, true, func(st *state, req request) (int, any, error) {
			if len(req.body) != 0 {
				return 0, nil, errInvalidInvite
			}
			invite, err := s.changeInvite(st, req.PathValue("invite"), route.to, &req.member)
			return http.StatusOK, invite, err
		}))
	}
	mux.HandleFunc("/api/v1/invites/redeem", s.inviteEndpoint(http.MethodPost, false, func(st *state, req request) (int, any, error) {
		var input redeemInput
		if err := decodeInviteBody(req.body, &input); err != nil {
			return 0, nil, err
		}
		now := s.inviteNow()
		result, err := s.redeemInvite(st, input, now)
		switch err {
		case errNotFound, errConflict, errInviteGone:
			err = s.limitRedeemFailure(st, now, err)
		}
		if result.State == pendingApproval {
			return http.StatusAccepted, result, err
		}
		return http.StatusOK, result, err
	}))

	// Use the same roster mutations for remote administration and local commands.
	mux.HandleFunc(admin+"members", s.inviteEndpoint(http.MethodPost, true, func(st *state, req request) (int, any, error) {
		var input struct {
			ID   string `json:"id"`
			Role Role   `json:"role"`
		}
		if err := decodeInviteBody(req.body, &input); err != nil {
			return 0, nil, errInvalidMember
		}
		token, err := st.addMember(input.ID, input.Role, &req.member, s.now())
		if err == nil {
			err = s.save(st)
		}
		return http.StatusOK, map[string]string{"token": token}, err
	}))
	mux.HandleFunc(admin+"members/{member}", s.inviteEndpoint(http.MethodDelete, true, func(st *state, req request) (int, any, error) {
		if len(req.body) != 0 {
			return 0, nil, fmt.Errorf("%w: member deletion does not accept a body", errInvalid)
		}
		if err := st.removeMember(req.PathValue("member"), &req.member, s.now()); err != nil {
			return 0, nil, err
		}
		return http.StatusOK, struct{}{}, s.save(st)
	}))
}

type headWriter struct{ http.ResponseWriter }

func (headWriter) Write(data []byte) (int, error) { return len(data), nil }

func (s *Store) inviteEndpoint(allow string, leadOnly bool, run func(*state, request) (int, any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w = headWriter{w}
		}
		validMethod := false
		for method := range strings.SplitSeq(allow, ", ") {
			validMethod = validMethod || r.Method == method
		}
		if !validMethod {
			w.Header().Set("Allow", allow)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var hash [sha256.Size]byte
		if leadOnly {
			var err error
			hash, err = bearerHash(r)
			if err != nil || strings.Contains(r.Header.Get("Authorization"), ",") {
				respond(w, nil, errUnauthorized)
				return
			}
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, inviteBodyLimit))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
			} else {
				writeError(w, http.StatusBadRequest, "could not read request body")
			}
			return
		}
		var result any
		var status int
		err = s.view(func(st *state) error {
			req := request{Request: r, body: body}
			if leadOnly {
				id, role, err := st.authenticate(hash)
				if err != nil {
					return err
				}
				if role != Lead {
					return errForbidden
				}
				req.member, req.role = id, role
			}
			var err error
			status, result, err = run(st, req)
			return err
		})
		var throttled redeemThrottled
		switch {
		case errors.As(err, &throttled):
			w.Header().Set("Retry-After", strconv.Itoa(throttled.retryAfter))
			writeError(w, http.StatusTooManyRequests, throttled.Error())
		case errors.Is(err, errInviteGone):
			writeError(w, http.StatusGone, errInviteGone.Error())
		case err != nil:
			respond(w, nil, err)
		default:
			if status == http.StatusAccepted {
				w.Header().Set("Retry-After", "5")
			}
			writeJSON(w, status, result)
		}
	}
}

func decodeInviteBody(body []byte, target any) error {
	var fields map[string]jsontext.Value
	if err := jsonv2.Unmarshal(body, &fields); err != nil || fields == nil {
		return errInvalidInvite
	}
	for _, v := range fields {
		if bytes.Equal(v, []byte("null")) {
			return errInvalidInvite
		}
	}
	if err := jsonv2.Unmarshal(body, target, jsonv2.RejectUnknownMembers(true)); err != nil {
		return errInvalidInvite
	}
	return nil
}

func invitePageQuery(query string) (after string, limit int, err error) {
	values, err := url.ParseQuery(query)
	if err != nil {
		return "", 0, errInvalidInvite
	}
	limit = DefaultInvitePage
	for name, v := range values {
		if len(v) != 1 {
			return "", 0, errInvalidInvite
		}
		switch name {
		case "after":
			if !inviteIDPattern.MatchString(v[0]) {
				return "", 0, errInvalidInvite
			}
			after = v[0]
		case "limit":
			n, err := strconv.ParseUint(v[0], 10, 8)
			if err != nil {
				return "", 0, errInvalidInvite
			}
			limit = int(n)
		default:
			return "", 0, errInvalidInvite
		}
	}
	if !ValidInvitePage(after, limit) {
		return "", 0, errInvalidInvite
	}
	return after, limit, nil
}
