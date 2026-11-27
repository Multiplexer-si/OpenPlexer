package hub

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
)

var invalidBearerCharacter = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func (s *Store) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		keys, err := s.PolicyKeys()
		respond(w, keys, err)
	})
	mux.HandleFunc("GET /api/v1/projects/{project}", s.endpoint(0, func(st *state, req request) (any, error) {
		p, ok := st.Projects[req.project()]
		if !ok {
			return nil, errNotFound
		}
		return p, nil
	}))
	mux.HandleFunc("GET /api/v1/projects/{project}/policy.bundle", s.policyBundleEndpoint)
	mux.HandleFunc("PUT /api/v1/projects/{project}/policy", s.endpoint(MaxPolicyBytes, func(st *state, req request) (any, error) {
		if req.role != Lead {
			return nil, errForbidden
		}
		signed, err := s.publishPolicy(st, req.project(), req.body, &req.member)
		if err != nil {
			return nil, err
		}
		return signed, s.save(st)
	}))
	s.inviteRoutes(mux)
	s.auditRoutes(mux)
	s.roomRoutes(mux)
	return mux
}

type request struct {
	*http.Request
	member string
	role   Role
	body   []byte
}

func (r request) project() string { return r.PathValue("project") }

func (s *Store) endpoint(limit int64, run func(*state, request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			respond(w, nil, errUnauthorized)
			return
		}
		var body []byte
		if limit > 0 {
			var err error
			body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
			if err != nil {
				var tooLarge *http.MaxBytesError
				if errors.As(err, &tooLarge) {
					writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
				} else {
					writeError(w, http.StatusBadRequest, "could not read request body")
				}
				return
			}
		}
		// The body cap takes precedence for a single 43-byte bearer.
		hash, err := rosterBearerHash(token)
		if err != nil {
			respond(w, nil, err)
			return
		}
		var result any
		err = s.view(func(st *state) error {
			member, role, err := st.authenticate(hash)
			if err != nil {
				return err
			}
			result, err = run(st, request{Request: r, member: member, role: role, body: body})
			return err
		})
		respond(w, result, err)
	}
}

func bearerHash(r *http.Request) ([sha256.Size]byte, error) {
	return rosterBearerHash(bearerToken(r))
}

func bearerToken(r *http.Request) string {
	auth := r.Header.Values("Authorization")
	if len(auth) == 1 {
		scheme, token, _ := strings.Cut(auth[0], " ")
		if strings.EqualFold(scheme, "Bearer") && len(token) == base64.RawURLEncoding.EncodedLen(32) {
			return token
		}
	}
	return ""
}

func rosterBearerHash(token string) ([sha256.Size]byte, error) {
	if token == "" || invalidBearerCharacter.MatchString(token) {
		return [sha256.Size]byte{}, errUnauthorized
	}
	return sha256.Sum256([]byte(token)), nil
}

func (st *state) authenticate(hash [sha256.Size]byte) (string, Role, error) {
	for id, member := range st.Members {
		if subtle.ConstantTimeCompare(hash[:], member.TokenHash[:]) == 1 {
			return id, member.Role, nil
		}
	}
	return "", "", errUnauthorized
}

func respond(w http.ResponseWriter, body any, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, body)
		return
	}
	switch {
	case errors.Is(err, errUnauthorized):
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, errUnauthorized.Error())
	case errors.Is(err, errForbidden):
		writeError(w, http.StatusForbidden, errForbidden.Error())
	case errors.Is(err, errNotFound):
		writeError(w, http.StatusNotFound, errNotFound.Error())
	case errors.Is(err, errConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, errInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "hub state unavailable")
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
