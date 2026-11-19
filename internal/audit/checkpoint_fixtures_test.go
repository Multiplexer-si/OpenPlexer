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

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const auditSchemaID = "urn:openplexer:spec:audit:v1:"

type checkpointSchemas struct {
	entry, header, payload *jsonschema.Schema
}

type checkpointInput struct {
	Export      json.RawMessage `json:"export"`
	Checkpoints []string        `json:"checkpoints"`
}

func checkpointRunner(t *testing.T, keys []map[string]any) func(*testing.T, json.RawMessage, json.RawMessage) {
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
		got := checkCheckpointFixture(t, schemas, keys, in)
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
func checkCheckpointFixture(t *testing.T, schemas checkpointSchemas, keys []map[string]any, in checkpointInput) map[string]any {
	var export map[string]any
	if err := json.Unmarshal(in.Export, &export); err != nil {
		return fixtureFailure("A6", 0)
	}
	entries, isList := export["entries"].([]any)
	cursor, hasCursor := export["next_cursor"]
	if !isList || !hasCursor || cursor != nil {
		return fixtureFailure("A6", 0)
	}
	wire, err := parse(in.Export)
	if err != nil {
		t.Fatal(err)
	}
	records, _ := wire.member("entries")
	slices.Reverse(entries)
	slices.Reverse(records.items)
	previous := GenesisHash
	for i, entry := range entries {
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
		computed, err := hashOf(previous, records.items[i])
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

func fixtureCanonicalObject(raw []byte) map[string]any {
	canonical, err := Canonical(raw)
	if err != nil || !bytes.Equal(raw, canonical) {
		return nil
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	return doc
}

func checkFixtureToken(schemas checkpointSchemas, keys []map[string]any, token string) (map[string]any, string) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, "K1"
	}
	headerBytes, payloadBytes, signature := fixtureSegment(parts[0]), fixtureSegment(parts[1]), fixtureSegment(parts[2])
	if headerBytes == nil || payloadBytes == nil || signature == nil {
		return nil, "K1"
	}
	header := fixtureCanonicalObject(headerBytes)
	if header == nil || schemas.header.Validate(header) != nil {
		return nil, "K2"
	}
	public := fixturePublicKey(keys, header["kid"].(string))
	if public == nil {
		return nil, "K3"
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(signature) != 64 || !ecdsa.Verify(public, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
		return nil, "K4"
	}
	body := fixtureCanonicalObject(payloadBytes)
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

func fixturePublicKey(keys []map[string]any, kid string) *ecdsa.PublicKey {
	var key map[string]any
	for _, candidate := range keys {
		if candidate["kid"] == kid {
			if key != nil {
				return nil
			}
			key = candidate
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
