// Command auditvectors regenerates the OpenPlexer checkpoint fixtures.
package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/multiplexer-si/openplexer/internal/audit"
)

const (
	fixturePath    = "spec/audit/v1/fixtures/checkpoint.json"
	checkpointType = "openplexer-audit-checkpoint+jws"
	workspace      = "wsp-checkpoint-test"
	issuedAt       = "2026-11-19T12:00:00.000Z"
)

type payload struct {
	Version     int    `json:"v"`
	WorkspaceID string `json:"workspace_id"`
	Entries     int    `json:"entries"`
	TipHash     string `json:"tip_hash"`
	IssuedAt    string `json:"issued_at"`
}

type entry struct {
	audit.Record
	Result string `json:"result"`
}

type testKey struct {
	kid     string
	private *ecdsa.PrivateKey
}

// The private scalars are public fixture data. Never use these keys outside tests.
func newTestKey(kid string, scalar byte) testKey {
	raw := make([]byte, 32)
	raw[31] = scalar
	return testKey{kid, must(ecdsa.ParseRawPrivateKey(elliptic.P256(), raw))}
}

var base64url = base64.RawURLEncoding

func (key testKey) jwk() map[string]string {
	point := must(key.private.PublicKey.Bytes())
	return map[string]string{
		"kty": "EC", "crv": "P-256", "alg": "ES256", "use": "sig", "kid": key.kid,
		"x": base64url.EncodeToString(point[1:33]),
		"y": base64url.EncodeToString(point[33:]),
	}
}

func (key testKey) sign(typ, kid string, body any) string {
	header := map[string]string{"alg": "ES256", "kid": kid, "typ": typ}
	segment := func(v any) string {
		return base64url.EncodeToString(must(audit.Canonical(must(json.Marshal(v)))))
	}
	input := segment(header) + "." + segment(body)
	digest := sha256.Sum256([]byte(input))
	// A nil reader selects deterministic RFC 6979 signing.
	der := must(key.private.Sign(nil, digest[:], crypto.SHA256))
	var pair struct{ R, S *big.Int }
	must(asn1.Unmarshal(der, &pair))
	var raw [64]byte
	pair.R.FillBytes(raw[:32])
	pair.S.FillBytes(raw[32:])
	return input + "." + base64url.EncodeToString(raw[:])
}

func changeSignature(token string) string {
	separator := strings.LastIndexByte(token, '.')
	raw := must(base64url.DecodeString(token[separator+1:]))
	raw[0] ^= 1
	return token[:separator+1] + base64url.EncodeToString(raw)
}

type fixtureCase struct {
	Name     string         `json:"name"`
	Why      string         `json:"why"`
	Input    map[string]any `json:"input"`
	Expected map[string]any `json:"expected"`
}

func vector(name, why string, records []any, checkpoints []string, expected map[string]any) fixtureCase {
	if checkpoints == nil {
		checkpoints = []string{}
	}
	return fixtureCase{name, why, map[string]any{
		"export":      map[string]any{"entries": records, "next_cursor": nil},
		"checkpoints": checkpoints,
	}, expected}
}

func success(entries int, checkpointEntries any) map[string]any {
	completeness := "not_proven"
	if checkpointEntries != nil {
		completeness = "through_checkpoint"
	}
	return map[string]any{
		"ok": true, "entries": entries, "checkpoint_entries": checkpointEntries, "completeness": completeness,
	}
}

func failure(rule string, checkpoint, recordNumber int, id string) map[string]any {
	result := map[string]any{"ok": false, "rule": rule}
	if checkpoint != 0 {
		result["checkpoint"] = checkpoint
	}
	if recordNumber != 0 {
		result["record_number"] = recordNumber
		result["broken_at"] = id
	}
	return result
}

func generate() []byte {
	previous := audit.GenesisHash
	var chain [4]entry
	for i := range chain {
		record := audit.Record{
			ID: strconv.Itoa(11 + 7*i), WorkspaceID: workspace, Action: "policy.updated",
			Target: "prj-checkpoint-test", Meta: map[string]any{"version": i + 1}, CreatedAt: issuedAt,
		}
		chain[i] = entry{must(audit.Seal(record, previous)), "updated"}
		previous = chain[i].Hash
	}
	first, second, third, fourth := chain[0], chain[1], chain[2], chain[3]
	full := []any{third, second, first}

	oldKey := newTestKey("checkpoint-test-old", 1)
	newKey := newTestKey("checkpoint-test-new", 2)
	tip := payload{1, workspace, 3, third.Hash, issuedAt}
	middle := payload{1, workspace, 2, second.Hash, issuedAt}
	tipToken := newKey.sign(checkpointType, newKey.kid, tip)
	middleToken := oldKey.sign(checkpointType, oldKey.kid, middle)
	zeroToken := oldKey.sign(checkpointType, oldKey.kid, payload{1, workspace, 0, audit.GenesisHash, issuedAt})
	aheadToken := newKey.sign(checkpointType, newKey.kid, payload{1, workspace, 4, fourth.Hash, issuedAt})

	altered := second
	altered.Target = "prj-altered"
	malformed := struct {
		entry
		Flagged string `json:"flagged"`
	}{second, "false"}
	rewritten := third
	rewritten.Target = "prj-rewritten"
	rewritten.Record = must(audit.Seal(rewritten.Record, second.Hash))
	conflicting := tip
	conflicting.TipHash = rewritten.Hash
	future := tip
	future.Version = 2
	wrongWorkspace := tip
	wrongWorkspace.WorkspaceID = "wsp-other-test"
	badDate := tip
	badDate.IssuedAt = "2026-02-30T12:00:00.000Z"
	yearZero := payload{1, workspace, 0, audit.GenesisHash, "0000-01-01T00:00:00.000Z"}
	policyBundle := json.RawMessage(`{"areas":[],"members":{},"project_id":"prj-checkpoint-test","repos":[],"roles":{},"version":1}`)

	cases := []fixtureCase{
		vector("no checkpoint", "The chain is consistent from genesis and completeness is not proven.", full, nil, success(3, nil)),
		vector("checkpoint at the tip", "The checkpoint covers all three exported records.", full, []string{tipToken}, success(3, 3)),
		vector("checkpoint in the middle", "The checkpoint covers only the first two records.", full, []string{middleToken}, success(3, 2)),
		vector("checkpoint at count zero", "A zero-count checkpoint authenticates the genesis hash of an empty log.", []any{}, []string{zeroToken}, success(0, 0)),
		vector("checkpoints across key rotation", "Both published keys verify and coverage uses the greatest count.", full, []string{tipToken, middleToken}, success(3, 3)),
		vector("altered record", "The second record has a changed target and its original hash.", []any{third, altered, first}, nil, failure("A3", 0, 2, second.ID)),
		vector("reordered records", "Swapping the two newest entries breaks the third record's previous link.", []any{second, third, first}, nil, failure("A5", 0, 2, third.ID)),
		vector("missing middle record", "The third record still links to the omitted second record.", []any{third, first}, nil, failure("A5", 0, 2, third.ID)),
		vector("truncated chain", "The export is a valid prefix, but the retained checkpoint requires its last record.", []any{second, first}, []string{tipToken}, failure("K9", 1, 0, "")),
		vector("malformed record", "The flagged field is a string instead of a boolean.", []any{third, malformed, first}, nil, failure("A1", 0, 2, second.ID)),
		vector("unsupported checkpoint version", "The signature verifies and the signed version is 2.", full, []string{newKey.sign(checkpointType, newKey.kid, future)}, failure("K5", 1, 0, "")),
		vector("bad signature", "The signature has a changed byte and the original header and payload.", full, []string{changeSignature(tipToken)}, failure("K4", 1, 0, "")),
		vector("policy-typed JWS", "A token signed with a known test key has the policy bundle type.", full, []string{newKey.sign("mp-policy+jws", newKey.kid, policyBundle)}, failure("K2", 1, 0, "")),
		vector("unknown kid", "The header names a key absent from the checkpoint JWKS.", full, []string{newKey.sign(checkpointType, "checkpoint-test-absent", tip)}, failure("K3", 1, 0, "")),
		vector("workspace mismatch", "The signed workspace differs from the workspace of the records.", full, []string{newKey.sign(checkpointType, newKey.kid, wrongWorkspace)}, failure("K7", 1, 1, first.ID)),
		vector("checkpoint beyond the chain", "The checkpoint claims a fourth record and the export has three.", full, []string{aheadToken}, failure("K9", 1, 0, "")),
		vector("contradicting checkpoints", "Both signatures verify and the two tips differ at the same count.", full, []string{tipToken, oldKey.sign(checkpointType, oldKey.kid, conflicting)}, failure("K8", 2, 0, "")),
		vector("filtered export", "The export starts at the second record, whose previous hash is not genesis.", []any{third, second}, nil, failure("A4", 0, 1, second.ID)),
		vector("empty filtered export", "Filtering out every record leaves a valid empty export whose completeness is not proven.", []any{}, nil, success(0, nil)),
		vector("empty filtered export with a later checkpoint", "The empty export passes chain checks, but the checkpoint requires three records.", []any{}, []string{tipToken}, failure("K9", 1, 0, "")),
		vector("padded compact JWS", "The signature segment ends in a padding character.", full, []string{tipToken + "="}, failure("K1", 1, 0, "")),
		vector("impossible issue date", "The signed date is February 30.", full, []string{newKey.sign(checkpointType, newKey.kid, badDate)}, failure("K6", 1, 0, "")),
		vector("issue date in year zero", "A checkpoint issue date cannot use year 0000.", []any{}, []string{oldKey.sign(checkpointType, oldKey.kid, yearZero)}, failure("K6", 1, 0, "")),
		vector("rewritten tip", "The chain verifies and reaches the signed count with a different hash.", []any{rewritten, second, first}, []string{tipToken}, failure("K10", 1, 3, third.ID)),
	}
	page := vector("paged export", "The cursor is a string. The audit export requires null.", full, nil, failure("A6", 0, 0, ""))
	page.Input["export"] = map[string]any{"entries": full, "next_cursor": "11"}
	overflow := vector("out-of-range number in an unknown export field", "The export allows any value for this field, including an out-of-range number read as infinity.", []any{}, nil, success(0, nil))
	overflow.Input["export"].(map[string]any)["extra"] = json.Number("1e400")

	duplicateExport := vector("repeated top-level member", "The export repeats an extension member.", []any{}, []string{zeroToken}, failure("A6", 0, 0, ""))
	duplicateExport.Input["export"] = json.RawMessage(`{"entries":[],"next_cursor":null,"note":"draft","note":"kept"}`)
	secondJSON := string(must(json.Marshal(second)))
	duplicateEntry := json.RawMessage(`{"result":"old",` + secondJSON[1:])
	duplicateMeta := json.RawMessage(strings.Replace(secondJSON, `"version":2`, `"version":1,"\u0076ersion":2`, 1))
	cases = append(cases, page, overflow, duplicateExport,
		vector("repeated entry member", "The second record repeats the display result.", []any{duplicateEntry, first}, []string{middleToken}, failure("A1", 0, 2, second.ID)),
		vector("repeated meta member", "The second record repeats a name in meta using two spellings of the same decoded name.", []any{duplicateMeta, first}, []string{middleToken}, failure("A1", 0, 2, second.ID)),
	)

	keysJSON := string(must(json.Marshal([]any{oldKey.jwk()})))
	duplicateJWKS := vector("duplicate keys member in the JWKS", "The trusted JWKS repeats the keys member, even though its last value contains the signer.", []any{}, []string{zeroToken}, failure("K3", 1, 0, ""))
	duplicateJWKS.Input["jwks_json"] = `{"keys":[],"keys":` + keysJSON + `}`
	nestedDuplicate := vector("duplicate member inside JWKS metadata", "Member names are compared after decoding escapes, even inside ignored arrays and objects.", []any{}, []string{zeroToken}, failure("K3", 1, 0, ""))
	nestedDuplicate.Input["jwks_json"] = `{"keys":` + keysJSON + `,"metadata":[{"a":1,"\u0061":2}]}`
	caseSensitiveJWKS := vector("JWKS members that differ in case", "The unrecognized uppercase KEYS member is distinct from keys.", []any{}, []string{zeroToken}, success(0, 0))
	caseSensitiveJWKS.Input["jwks_json"] = `{"keys":` + keysJSON + `,"KEYS":[]}`
	cases = append(cases, duplicateJWKS, nestedDuplicate, caseSensitiveJWKS)

	data := map[string]any{
		"contract": "audit", "version": 1, "operation": "verify-checkpoints",
		"description": "OpenPlexer audit exports and checkpoints. The signing keys are public test keys only.",
		"shared":      map[string]any{"jwks": map[string]any{"keys": []any{oldKey.jwk(), newKey.jwk()}}},
		"cases":       cases,
	}
	return append(must(json.MarshalIndent(data, "", "  ")), '\n')
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func writeFixture(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".checkpoint-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if _, err := temp.Write(data); err != nil {
		return err
	}
	if err := temp.Chmod(0644); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

func main() {
	if err := writeFixture(fixturePath, generate()); err != nil {
		log.Fatal(err)
	}
}
