package audit

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/base64"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/multiplexer-si/openplexer/internal/policy"
)

const (
	CheckpointType = "openplexer-audit-checkpoint+jws"
	maxSafeInteger = 1<<53 - 1
)

// Checkpoint is the signed payload of a v1 audit checkpoint.
type Checkpoint struct {
	Version     int    `json:"v"`
	WorkspaceID string `json:"workspace_id"`
	Entries     int    `json:"entries"`
	TipHash     string `json:"tip_hash"`
	IssuedAt    string `json:"issued_at"`
}

type claim struct {
	workspace string
	count     int64
}

type checkpoint struct {
	claim
	tip string
}

func verifyCheckpoints(records []value, jwks []byte, tokens []string) VerifyResult {
	result := VerifyResult{OK: true, Entries: len(records), Completeness: "not_proven"}
	if len(tokens) == 0 {
		return result
	}

	keys, _ := parse(jwks)
	claims := make([]checkpoint, len(tokens))
	for i, token := range tokens {
		cp, failed := readCheckpoint(token, keys)
		if failed.Rule != "" {
			failed.Checkpoint = i + 1
			return failed
		}
		for j, record := range records {
			workspace, _ := record.member("workspace_id")
			if found := string(workspace.appendCanonical(nil)); found != cp.workspace {
				message := fmt.Sprintf("expected workspace %s, found %s. Check that the checkpoint and export came from the same team", cp.workspace, found)
				return (VerifyResult{Rule: "K7", Checkpoint: i + 1, Message: message}).atRecord(record, j+1)
			}
		}
		claims[i] = cp
	}

	type assertion struct {
		tip      string
		position int
	}
	tips := make(map[claim]assertion)
	for i, cp := range claims {
		if prior, exists := tips[cp.claim]; exists {
			if prior.tip != cp.tip {
				message := fmt.Sprintf("expected tip %s from checkpoint %d at count %d, found %s. Compare independently kept checkpoints", prior.tip, prior.position, cp.count, cp.tip)
				return VerifyResult{Rule: "K8", Checkpoint: i + 1, Message: message}
			}
			continue
		}
		tips[cp.claim] = assertion{cp.tip, i + 1}
	}

	covered := 0
	for i, cp := range claims {
		if cp.count > int64(len(records)) {
			message := fmt.Sprintf("expected at least %d records, found %d. Check for a missing suffix or an older export", cp.count, len(records))
			return VerifyResult{Rule: "K9", Checkpoint: i + 1, Message: message}
		}
		count := int(cp.count)
		expected := GenesisHash
		var record value
		if count > 0 {
			record = records[count-1]
			hash, _ := record.member("hash")
			expected, _ = hash.str()
		}
		if expected != cp.tip {
			message := fmt.Sprintf("expected signed tip %s, found %s at count %d. Compare the export with a kept copy", cp.tip, expected, count)
			if count == 0 {
				message = fmt.Sprintf("expected genesis hash %s at count 0, found signed tip %s. Check the checkpoint", GenesisHash, cp.tip)
			}
			failed := VerifyResult{Rule: "K10", Checkpoint: i + 1, Message: message}
			if count > 0 {
				failed = failed.atRecord(record, count)
			}
			return failed
		}
		covered = max(covered, count)
	}
	result.CheckpointEntries = &covered
	result.Completeness = "through_checkpoint"
	return result
}

func readCheckpoint(token string, keys value) (checkpoint, VerifyResult) {
	fail := func(rule, message string) (checkpoint, VerifyResult) {
		return checkpoint{}, VerifyResult{Rule: rule, Message: message}
	}
	parts := strings.SplitN(token, ".", 4)
	if len(parts) != 3 {
		return fail("K1", "expected three compact JWS segments, found an invalid segment count. Check the checkpoint file")
	}
	var decoded [3][]byte
	for i, part := range parts {
		raw, err := base64.RawURLEncoding.Strict().DecodeString(part)
		if err != nil || part == "" || strings.ContainsAny(part, "\r\n") {
			return fail("K1", fmt.Sprintf("expected unpadded base64url in segment %d, found invalid encoding. Copy the compact token exactly", i+1))
		}
		decoded[i] = raw
	}

	header, ok := canonicalObject(decoded[0])
	alg, _ := header.member("alg")
	typ, _ := header.member("typ")
	kid, _ := header.member("kid")
	if !ok || len(header.members) != 3 || !alg.isText("ES256") || kid.kind != kindString || len(kid.units) == 0 {
		return fail("K2", "expected a canonical checkpoint header with ES256 and a nonempty kid, found an invalid header. Use an audit checkpoint token")
	}
	if !typ.isText(CheckpointType) {
		return fail("K2", fmt.Sprintf("expected typ %q, found %s. Use an audit checkpoint token", CheckpointType, typ.appendCanonical(nil)))
	}
	key, err := checkpointKey(keys, kid)
	if err != nil {
		return fail("K3", fmt.Sprintf("expected one public ES256 key for kid %s, found %v. Check the trusted checkpoint JWKS", kid.appendCanonical(nil), err))
	}
	if err := policy.VerifyES256(key, parts[0]+"."+parts[1], decoded[2]); err != nil {
		return fail("K4", fmt.Sprintf("expected a valid ES256 signature, found a signature error (%v). Check the token and trusted key", err))
	}
	body, ok := canonicalObject(decoded[1])
	if !ok {
		return fail("K6", "expected one canonical payload object, found invalid JSON or a different encoding. Obtain the original checkpoint")
	}
	version, present := body.member("v")
	if version.kind != kindNumber || version.number != 1 {
		found := "no version"
		if present {
			found = string(version.appendCanonical(nil))
		}
		return fail("K5", fmt.Sprintf("expected checkpoint version 1, found %s. Use a v1 audit checkpoint", found))
	}
	workspace, _ := body.member("workspace_id")
	count, _ := body.member("entries")
	tip, _ := body.member("tip_hash")
	hash, _ := tip.str()
	issued, _ := body.member("issued_at")
	date, _ := issued.str()
	if len(body.members) != 5 || workspace.kind != kindString {
		return fail("K6", "expected exactly the five v1 fields including a string workspace_id, found invalid payload fields. Check the checkpoint producer")
	}
	if count.kind != kindNumber || count.number < 0 || count.number > maxSafeInteger || math.Trunc(count.number) != count.number {
		return fail("K6", "expected entries to be a nonnegative safe integer, found a missing or invalid count. Check the checkpoint producer")
	}
	if len(hash) != 64 || strings.Trim(hash, "0123456789abcdef") != "" {
		return fail("K6", "expected tip_hash to be 64 lowercase hex characters, found an invalid hash. Check the checkpoint producer")
	}
	if t, err := time.Parse(CreatedAtLayout, date); err != nil || t.Year() == 0 || t.Format(CreatedAtLayout) != date {
		return fail("K6", "expected issued_at to be a real UTC timestamp with a year from 0001 through 9999 and three fractional digits, found an invalid date or format. Check the checkpoint producer")
	}
	return checkpoint{claim{string(workspace.appendCanonical(nil)), int64(count.number)}, hash}, VerifyResult{}
}

func canonicalObject(raw []byte) (value, bool) {
	doc, err := parse(raw)
	return doc, err == nil && doc.kind == kindObject && doc.duplicateKey() == "" && bytes.Equal(raw, doc.appendCanonical(nil))
}

func (v value) isText(s string) bool {
	return v.kind == kindString && slices.Equal(v.units, text(s).units)
}

func checkpointKey(doc, kid value) (*ecdsa.PublicKey, error) {
	keys, _ := doc.member("keys")
	if name := doc.duplicateKey(); name != "" {
		return nil, fmt.Errorf("repeated JSON member %s", name)
	}
	if keys.kind != kindArray {
		return nil, fmt.Errorf("an invalid key set")
	}
	var selected value
	matches := 0
	for _, key := range keys.items {
		if key.kind != kindObject {
			return nil, fmt.Errorf("a JWKS entry that is not an object")
		}
		id, _ := key.member("kid")
		if id.kind == kindString && slices.Equal(id.units, kid.units) {
			matches++
			selected = key
		}
	}
	if matches != 1 {
		return nil, fmt.Errorf("%d matching keys", matches)
	}
	// Pass only the parsed fields used by the shared ES256 key validator.
	jwk := make(map[string]any)
	for _, name := range []string{"kty", "crv", "alg", "use", "x", "y"} {
		if field, present := selected.member(name); present {
			jwk[name], _ = field.str()
		}
	}
	if _, private := selected.member("d"); private {
		jwk["d"] = nil
	}
	if ops, present := selected.member("key_ops"); present {
		items := make([]any, len(ops.items))
		for i, op := range ops.items {
			items[i], _ = op.str()
		}
		jwk["key_ops"] = items
	}
	return policy.ES256PublicKey(jwk)
}
