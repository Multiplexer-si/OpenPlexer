package hub

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/multiplexer-si/openplexer/internal/audit"
)

const auditCheckpointHeader = "OpenPlexer-Audit-Checkpoint"

func (st *state) ensureAudit() (bool, error) {
	if st.WorkspaceID != "" {
		return false, nil
	}
	// A store created before audit support has none of these fields.
	if st.CheckpointKey != nil || len(st.CheckpointKeys) != 0 || len(st.Audit) != 0 {
		return false, errors.New("hub audit state is missing its workspace ID")
	}
	if err := st.addCheckpointKey(); err != nil {
		return false, err
	}
	st.WorkspaceID = rand.Text()
	return true, nil
}

func checkpointKeyID(key *ecdsa.PublicKey) string {
	return "audit-" + keyID(key)
}

func (st *state) addCheckpointKey() error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	private, err := key.Bytes()
	if err != nil {
		return err
	}
	if bytes.Equal(private, st.SigningKey) {
		return errors.New("checkpoint key must be distinct from the policy key")
	}
	st.CheckpointKey = private
	st.CheckpointKeys = append(st.CheckpointKeys, publicJWK(&key.PublicKey, checkpointKeyID(&key.PublicKey)))
	return nil
}

func (st *state) checkpointSigningKey() (*ecdsa.PrivateKey, error) {
	if bytes.Equal(st.CheckpointKey, st.SigningKey) {
		return nil, errors.New("checkpoint key must be distinct from the policy key")
	}
	key, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), st.CheckpointKey)
	if err != nil {
		return nil, fmt.Errorf("read checkpoint signing key: %w", err)
	}
	active := publicJWK(&key.PublicKey, checkpointKeyID(&key.PublicKey))
	if len(st.CheckpointKeys) == 0 || !maps.Equal(st.CheckpointKeys[len(st.CheckpointKeys)-1], active) {
		return nil, errors.New("checkpoint signing key must be the newest published key")
	}
	return key, nil
}

// appendAudit adds a record to the same pending state write as the action.
// The caller holds the store lock across the append and save.
func (st *state) appendAudit(record audit.Record, now time.Time) error {
	if _, err := st.ensureAudit(); err != nil {
		return err
	}
	record.ID = strconv.Itoa(len(st.Audit) + 1)
	record.WorkspaceID = st.WorkspaceID
	record.CreatedAt = now.UTC().Format(audit.CreatedAtLayout)
	sealed, err := audit.Seal(record, st.auditTip())
	if err != nil {
		return err
	}
	st.Audit = append(st.Audit, sealed)
	return nil
}

func (st *state) auditTip() string {
	if len(st.Audit) == 0 {
		return audit.GenesisHash
	}
	return st.Audit[len(st.Audit)-1].Hash
}

// AuditExport holds the three files needed for offline audit verification.
type AuditExport struct {
	Export     json.RawMessage
	Checkpoint string
	JWKS       json.RawMessage
}

type auditEntry struct {
	audit.Record
	Result        string  `json:"result"`
	AgentMemberID *string `json:"agent_member_id,omitempty"`
}

type checkpointJWKS struct {
	Keys []map[string]string `json:"keys"`
}

// ExportAudit takes a consistent snapshot of the log, checkpoint, and keys
// from the data directory.
func (s *Store) ExportAudit() (AuditExport, error) {
	var export AuditExport
	err := s.view(func(st *state) error {
		var err error
		export, err = s.exportAudit(st)
		return err
	})
	return export, err
}

func (s *Store) prepareAudit(st *state) error {
	initialized, err := st.ensureAudit()
	if err != nil {
		return err
	}
	if initialized {
		return s.save(st)
	}
	return nil
}

func (s *Store) exportAudit(st *state) (AuditExport, error) {
	if err := s.prepareAudit(st); err != nil {
		return AuditExport{}, err
	}
	key, err := st.checkpointSigningKey()
	if err != nil {
		return AuditExport{}, err
	}
	entries := make([]auditEntry, len(st.Audit))
	for i, record := range st.Audit {
		entry := auditEntry{Record: record, Result: audit.ActionResult(record.Action)}
		if record.AgentSessionID != nil {
			if owner, _, ok := strings.Cut(*record.AgentSessionID, "/"); ok {
				entry.AgentMemberID = &owner
			}
		}
		entries[len(entries)-1-i] = entry
	}
	export, err := json.Marshal(struct {
		Entries    []auditEntry `json:"entries"`
		NextCursor *string      `json:"next_cursor"`
	}{Entries: entries})
	if err != nil {
		return AuditExport{}, err
	}
	payload, err := json.Marshal(audit.Checkpoint{
		Version: 1, WorkspaceID: st.WorkspaceID, Entries: len(entries), TipHash: st.auditTip(),
		IssuedAt: s.now().UTC().Format(audit.CreatedAtLayout),
	})
	if err != nil {
		return AuditExport{}, err
	}
	canonical, err := audit.Canonical(payload)
	if err != nil {
		return AuditExport{}, err
	}
	token, err := signES256(key, audit.CheckpointType, checkpointKeyID(&key.PublicKey), canonical)
	if err != nil {
		return AuditExport{}, err
	}
	keys, err := json.Marshal(checkpointJWKS{st.CheckpointKeys})
	if err != nil {
		return AuditExport{}, err
	}
	return AuditExport{Export: export, Checkpoint: token, JWKS: keys}, nil
}

func (s *Store) auditRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/audit-jwks.json", func(w http.ResponseWriter, r *http.Request) {
		var keys checkpointJWKS
		err := s.view(func(st *state) error {
			if err := s.prepareAudit(st); err != nil {
				return err
			}
			keys.Keys = st.CheckpointKeys
			return nil
		})
		respond(w, keys, err)
	})
	mux.HandleFunc("GET /api/v1/audit/export", func(w http.ResponseWriter, r *http.Request) {
		s.endpoint(0, func(st *state, req request) (any, error) {
			if req.role != Lead {
				return nil, errForbidden
			}
			snapshot, err := s.exportAudit(st)
			if err != nil {
				return nil, err
			}
			w.Header().Set(auditCheckpointHeader, snapshot.Checkpoint)
			return snapshot.Export, nil
		})(w, r)
	})
}
