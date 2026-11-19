package audit

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const auditSchemaID = "urn:openplexer:spec:audit:v1:"

func TestMissingGenesisRuleAppliesToNonemptyExports(t *testing.T) {
	for _, path := range []string{
		filepath.Join(specDir, "audit", "README.md"),
		filepath.Join("..", "..", "docs", "trust-model.md"),
	} {
		if !bytes.Contains(readFile(t, path), []byte("nonempty filtered export that omits genesis")) {
			t.Errorf("%s must qualify the missing-genesis claim with nonempty", path)
		}
	}
}

type checkpointSchemas struct {
	entry, header, payload *jsonschema.Schema
}

type checkpointInput struct {
	Export      json.RawMessage `json:"export"`
	Checkpoints []string        `json:"checkpoints"`
	JWKSJSON    *string         `json:"jwks_json"`
}

func checkpointRunner(t *testing.T, sharedJWKS json.RawMessage) func(*testing.T, json.RawMessage, json.RawMessage) {
	t.Helper()
	compiler := jsonschema.NewCompiler()
	for _, name := range []string{"export", "checkpoint-protected-header", "checkpoint-payload"} {
		var schema any
		decode(t, readFile(t, filepath.Join(specDir, "audit", "v1", name+".schema.json")), &schema)
		if err := compiler.AddResource(auditSchemaID+name, schema); err != nil {
			t.Fatal(err)
		}
	}
	compile := func(name string) *jsonschema.Schema {
		schema, err := compiler.Compile(auditSchemaID + name)
		if err != nil {
			t.Fatal(err)
		}
		return schema
	}
	schemas := checkpointSchemas{
		entry:   compile("export#/properties/entries/items"),
		header:  compile("checkpoint-protected-header"),
		payload: compile("checkpoint-payload"),
	}
	return func(t *testing.T, input, expected json.RawMessage) {
		var in checkpointInput
		decode(t, input, &in)
		jwks := sharedJWKS
		if in.JWKSJSON != nil {
			jwks = json.RawMessage(*in.JWKSJSON)
		}
		got := checkCheckpointFixture(t, schemas, jwks, in)
		var want any
		decode(t, expected, &want)
		encoded, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		var result any
		decode(t, encoded, &result)
		if !reflect.DeepEqual(result, want) {
			t.Errorf("result = %s, want %s", encoded, expected)
		}
	}
}

// checkCheckpointFixture evaluates the published cases using the published
// schemas, the audit hash code, and the Go crypto package.
func checkCheckpointFixture(t *testing.T, schemas checkpointSchemas, jwks json.RawMessage, in checkpointInput) map[string]any {
	export, err := parse(in.Export)
	if err != nil {
		return fixtureFailure("A6", 0)
	}
	records, _ := export.member("entries")
	cursor, hasCursor := export.member("next_cursor")
	if records.kind != kindArray || !hasCursor || cursor.kind != kindNull {
		return fixtureFailure("A6", 0)
	}
	slices.Reverse(records.items)
	entries := make([]any, len(records.items))
	previous := GenesisHash
	for i, wireRecord := range records.items {
		entry := fixtureJSON(wireRecord)
		entries[i] = entry
		if schemas.entry.Validate(entry) != nil {
			return atRecord(fixtureFailure("A1", 0), entries, i)
		}
		record := entry.(map[string]any)
		if record["prev_hash"] != previous {
			rule := "A5"
			if i == 0 {
				rule = "A4"
			}
			return atRecord(fixtureFailure(rule, 0), entries, i)
		}
		computed, err := hashOf(previous, wireRecord)
		if err != nil {
			t.Fatal(err)
		}
		if record["hash"] != computed {
			return atRecord(fixtureFailure("A3", 0), entries, i)
		}
		previous = computed
	}

	type position struct {
		workspace string
		count     int64
	}
	type claim struct {
		position
		tip string
	}
	var keys []value
	if len(in.Checkpoints) > 0 {
		keys = fixtureKeys(jwks)
	}
	claims := make([]claim, len(in.Checkpoints))
	for i, token := range in.Checkpoints {
		body, rule := checkFixtureToken(schemas, keys, token)
		if rule != "" {
			return fixtureFailure(rule, i+1)
		}
		workspace := body["workspace_id"].(string)
		for j, entry := range entries {
			if entry.(map[string]any)["workspace_id"] != workspace {
				return atRecord(fixtureFailure("K7", i+1), entries, j)
			}
		}
		claims[i] = claim{position{workspace, int64(body["entries"].(float64))}, body["tip_hash"].(string)}
	}
	tips := map[position]string{}
	for i, claim := range claims {
		if prior, seen := tips[claim.position]; seen && prior != claim.tip {
			return fixtureFailure("K8", i+1)
		}
		tips[claim.position] = claim.tip
	}
	var covered int64
	for i, claim := range claims {
		if claim.count > int64(len(entries)) {
			return fixtureFailure("K9", i+1)
		}
		if claim.count == 0 && claim.tip != GenesisHash {
			return fixtureFailure("K10", i+1)
		}
		if claim.count > 0 {
			index := int(claim.count - 1)
			if entries[index].(map[string]any)["hash"] != claim.tip {
				return atRecord(fixtureFailure("K10", i+1), entries, index)
			}
		}
		covered = max(covered, claim.count)
	}

	result := map[string]any{"ok": true, "entries": len(entries), "checkpoint_entries": nil, "completeness": "not_proven"}
	if len(claims) > 0 {
		result["checkpoint_entries"] = covered
		result["completeness"] = "through_checkpoint"
	}
	return result
}

func fixtureJSON(v value) any {
	switch v.kind {
	case kindBool:
		return v.flag
	case kindNumber:
		return v.number
	case kindString:
		s, _ := v.str()
		return s
	case kindArray:
		items := make([]any, len(v.items))
		for i, item := range v.items {
			items[i] = fixtureJSON(item)
		}
		return items
	case kindObject:
		members := make(map[string]any, len(v.members))
		for _, m := range v.members {
			members[string(utf16.Decode(m.key))] = fixtureJSON(m.value)
		}
		return members
	default:
		return nil
	}
}

func fixtureFailure(rule string, checkpoint int) map[string]any {
	result := map[string]any{"ok": false, "rule": rule}
	if checkpoint > 0 {
		result["checkpoint"] = checkpoint
	}
	return result
}

func atRecord(result map[string]any, entries []any, index int) map[string]any {
	result["record_number"] = index + 1
	record, _ := entries[index].(map[string]any)
	if id, ok := record["id"].(string); ok {
		result["broken_at"] = id
	}
	return result
}

func fixtureSegment(segment string) []byte {
	decoded, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil || segment == "" || base64.RawURLEncoding.EncodeToString(decoded) != segment {
		return nil
	}
	return decoded
}

func fixtureCanonicalObject(raw []byte) value {
	doc, err := parse(raw)
	if err != nil || doc.kind != kindObject || !bytes.Equal(raw, doc.appendCanonical(nil)) {
		return value{}
	}
	return doc
}

func checkFixtureToken(schemas checkpointSchemas, keys []value, token string) (map[string]any, string) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, "K1"
	}
	headerBytes, payloadBytes, signature := fixtureSegment(parts[0]), fixtureSegment(parts[1]), fixtureSegment(parts[2])
	if headerBytes == nil || payloadBytes == nil || signature == nil {
		return nil, "K1"
	}
	header := fixtureCanonicalObject(headerBytes)
	if schemas.header.Validate(fixtureJSON(header)) != nil {
		return nil, "K2"
	}
	kid, _ := header.member("kid")
	public := fixturePublicKey(keys, kid)
	if public == nil {
		return nil, "K3"
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(signature) != 64 || !ecdsa.Verify(public, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
		return nil, "K4"
	}
	body, _ := fixtureJSON(fixtureCanonicalObject(payloadBytes)).(map[string]any)
	if body == nil {
		return nil, "K6"
	}
	if body["v"] != float64(1) {
		return nil, "K5"
	}
	if schemas.payload.Validate(body) != nil {
		return nil, "K6"
	}
	if _, err := time.Parse(CreatedAtLayout, body["issued_at"].(string)); err != nil {
		return nil, "K6"
	}
	return body, ""
}

func fixtureKeys(raw []byte) []value {
	r := reader{text: raw}
	jwks, err := r.parse()
	if err != nil || r.repeatedMember {
		return nil
	}
	keys, _ := jwks.member("keys")
	if keys.kind != kindArray {
		return nil
	}
	for _, key := range keys.items {
		if key.kind != kindObject {
			return nil
		}
	}
	return keys.items
}

func fixturePublicKey(keys []value, kid value) *ecdsa.PublicKey {
	var key map[string]any
	for _, candidate := range keys {
		candidateID, _ := candidate.member("kid")
		if candidateID.kind == kindString && slices.Equal(candidateID.units, kid.units) {
			if key != nil {
				return nil
			}
			key = fixtureJSON(candidate).(map[string]any)
		}
	}
	if key["kty"] != "EC" || key["crv"] != "P-256" {
		return nil
	}
	_, private := key["d"]
	alg, hasAlg := key["alg"]
	use, hasUse := key["use"]
	ops, hasOps := key["key_ops"]
	if private || (hasAlg && alg != "ES256") || (hasUse && use != "sig") || (hasOps && !reflect.DeepEqual(ops, []any{"verify"})) {
		return nil
	}
	x, _ := key["x"].(string)
	y, _ := key["y"].(string)
	xb, yb := fixtureSegment(x), fixtureSegment(y)
	if len(xb) != 32 || len(yb) != 32 {
		return nil
	}
	point := append(append([]byte{4}, xb...), yb...)
	public, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), point)
	if err != nil {
		return nil
	}
	return public
}
