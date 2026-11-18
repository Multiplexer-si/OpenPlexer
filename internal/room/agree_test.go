package room

import (
	"bytes"
	"encoding/json"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const idPrefix = "urn:openplexer:spec:room:v1:"

// decodeDoubles reads text as JSON.parse does: a key matches with case, the
// last of a repeated key wins, and a number is a double.
func decodeDoubles(t *testing.T, text []byte) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(text, &value); err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return value
}

func compileSchemas(t *testing.T) map[string]*jsonschema.Schema {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(specDir, "*.schema.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("found no schema under %s: %v", specDir, err)
	}
	compiler := jsonschema.NewCompiler()
	ids := map[string]string{}
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".schema.json")
		ids[name] = idPrefix + name
		if err := compiler.AddResource(ids[name], decodeDoubles(t, readFile(t, path))); err != nil {
			t.Fatal(err)
		}
	}
	schemas := map[string]*jsonschema.Schema{}
	for name, id := range ids {
		schema, err := compiler.Compile(id)
		if err != nil {
			t.Fatal(err)
		}
		schemas[name] = schema
	}
	return schemas
}

func variants(value any) []any {
	out := []any{nil, true, 1.0, 1.5, "", "x", []any{}, map[string]any{}}
	switch value := value.(type) {
	case string:
		out = append(out, value+"\n", strings.ToLower(value), strings.ToUpper(value))
	case map[string]any:
		for _, key := range slices.Sorted(maps.Keys(value)) {
			without := maps.Clone(value)
			delete(without, key)
			out = append(out, without)
			for _, changed := range variants(value[key]) {
				with := maps.Clone(value)
				with[key] = changed
				out = append(out, with)
			}
		}
		with := maps.Clone(value)
		with["unknown_field"] = 1.0
		out = append(out, with)
	case []any:
		for i := range value {
			for _, changed := range variants(value[i]) {
				with := slices.Clone(value)
				with[i] = changed
				out = append(out, with)
			}
		}
	}
	return out
}

type pair struct {
	key   string
	value any
}

// ordered copies a decoded value, with each object as a []pair, so that a
// text can hold one key twice.
func ordered(value any) any {
	switch value := value.(type) {
	case map[string]any:
		members := []pair{}
		for _, key := range slices.Sorted(maps.Keys(value)) {
			members = append(members, pair{key, ordered(value[key])})
		}
		return members
	case []any:
		out := make([]any, len(value))
		for i := range value {
			out[i] = ordered(value[i])
		}
		return out
	}
	return value
}

func writeOrdered(t *testing.T, out *bytes.Buffer, value any) {
	switch value := value.(type) {
	case []pair:
		out.WriteByte('{')
		for i, m := range value {
			if i > 0 {
				out.WriteByte(',')
			}
			writeOrdered(t, out, m.key)
			out.WriteByte(':')
			writeOrdered(t, out, m.value)
		}
		out.WriteByte('}')
	case []any:
		out.WriteByte('[')
		for i, element := range value {
			if i > 0 {
				out.WriteByte(',')
			}
			writeOrdered(t, out, element)
		}
		out.WriteByte(']')
	default:
		text, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(text)
	}
}

func otherCase(key string) []string {
	var out []string
	for _, other := range []string{strings.ToUpper(key), strings.ToUpper(key[:1]) + key[1:]} {
		if other != key && !slices.Contains(out, other) {
			out = append(out, other)
		}
	}
	return out
}

// keyVariants lists copies of value in which one object has a key in another
// case or a key twice, next to the same key with a value of another type.
func keyVariants(value any) []any {
	var out []any
	switch value := value.(type) {
	case []pair:
		with := func(i int, m pair, before, after []pair) {
			changed := slices.Concat(before, value[:i], []pair{m}, value[i+1:], after)
			out = append(out, changed)
		}
		for i, m := range value {
			for _, deeper := range keyVariants(m.value) {
				with(i, pair{m.key, deeper}, nil, nil)
			}
			for _, wrong := range []any{nil, "x", []pair{}} {
				with(i, pair{m.key, wrong}, nil, []pair{m})
				with(i, m, nil, []pair{{m.key, wrong}})
				for _, other := range otherCase(m.key) {
					moved := pair{other, m.value}
					with(i, pair{m.key, wrong}, nil, []pair{moved})
					with(i, pair{m.key, wrong}, []pair{moved}, nil)
					with(i, m, nil, []pair{{other, wrong}})
				}
			}
			for _, other := range otherCase(m.key) {
				with(i, pair{other, m.value}, nil, nil)
			}
		}
	case []any:
		for i := range value {
			for _, deeper := range keyVariants(value[i]) {
				changed := slices.Clone(value)
				changed[i] = deeper
				out = append(out, changed)
			}
		}
	}
	return out
}

func documents(t *testing.T, seed any) [][]byte {
	var texts [][]byte
	for _, doc := range append([]any{seed}, variants(seed)...) {
		text, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		texts = append(texts, text)
	}
	for _, doc := range keyVariants(ordered(seed)) {
		var text bytes.Buffer
		writeOrdered(t, &text, doc)
		texts = append(texts, text.Bytes())
	}
	return texts
}

func fixtureInputs(t *testing.T, schema string) []any {
	t.Helper()
	var seeds []any
	paths, _ := filepath.Glob(filepath.Join(specDir, "fixtures", "*.json"))
	for _, path := range paths {
		var file fixtureFile
		if err := json.Unmarshal(readFile(t, path), &file); err != nil {
			t.Fatal(err)
		}
		if file.Operation == "validate" && file.Schema == idPrefix+schema {
			for _, c := range file.Cases {
				seeds = append(seeds, decodeDoubles(t, c.Input))
			}
		}
	}
	examples, _ := filepath.Glob(filepath.Join(specDir, "examples", schema, "*.json"))
	for _, path := range examples {
		seeds = append(seeds, decodeDoubles(t, readFile(t, path)))
	}
	if len(seeds) == 0 {
		t.Fatalf("found no fixture input and no example of %s", schema)
	}
	return seeds
}

func payloadOf(doc any) (any, bool) {
	o, _ := doc.(map[string]any)
	payload, ok := o["payload"]
	return payload, ok
}

// messageVerdict is the verdict of the envelope schema and, for a resume or an
// error, of the schema of its payload.
func messageVerdict(schemas map[string]*jsonschema.Schema, doc any) error {
	if err := schemas["envelope"].Validate(doc); err != nil {
		return err
	}
	payload, _ := payloadOf(doc)
	switch doc.(map[string]any)["type"] {
	case "resume":
		return schemas["resume"].Validate(payload)
	case "error":
		return schemas["error"].Validate(payload)
	}
	return nil
}

func TestParsersAgreeWithTheSchemas(t *testing.T) {
	schemas := compileSchemas(t)
	envelopes := fixtureInputs(t, "envelope")
	claim := mustEvent(t, eventPayload("claim", 5, `{"item":"i","paths":["src/**"],"intent":"x","ttl_seconds":600}`))
	hub := Sender{ID: "hub", Kind: Server}
	for _, built := range []Message{
		EventMessage(claim),
		ResumeMessage(Sender{ID: "dmn", Kind: Daemon}, "mis", 41),
		ErrorMessage(hub, "mis", "2026-01-15T12:00:00.000Z", Error{Code: RateLimit, Message: "slow down", ClientEventID: "cev-1"}),
	} {
		envelopes = append(envelopes, decodeDoubles(t, mustMarshal(t, built)))
	}
	for _, subject := range []struct {
		name    string
		seeds   []any
		verdict func(doc any) error
		parse   func(text []byte) error
	}{
		{"room-event", fixtureInputs(t, "room-event"), schemas["room-event"].Validate, validators[idPrefix+"room-event"]},
		{"resume", fixtureInputs(t, "resume"), schemas["resume"].Validate, validators[idPrefix+"resume"]},
		{"error", fixtureInputs(t, "error"), schemas["error"].Validate, validators[idPrefix+"error"]},
		{"envelope", envelopes, schemas["envelope"].Validate, validators[idPrefix+"envelope"]},
		{"message", envelopes, func(doc any) error { return messageVerdict(schemas, doc) }, func(text []byte) error { _, err := ParseMessage(text); return err }},
	} {
		t.Run(subject.name, func(t *testing.T) {
			compared, invalid := 0, 0
			for _, seed := range subject.seeds {
				for _, text := range documents(t, seed) {
					schemaErr := subject.verdict(decodeDoubles(t, text))
					parseErr := subject.parse(text)
					compared++
					if schemaErr != nil {
						invalid++
					}
					if (schemaErr == nil) != (parseErr == nil) {
						t.Errorf("the schema says %v and the parser says %v for %s", schemaErr, parseErr, text)
					}
				}
			}
			if invalid == 0 || invalid == compared {
				t.Errorf("%d of %d documents are invalid, so the test compares one verdict only", invalid, compared)
			}
			t.Logf("compared %d documents, %d of them invalid", compared, invalid)
		})
	}
	t.Run("the event that is read is the payload member", func(t *testing.T) {
		delivered := 0
		for _, seed := range envelopes {
			for _, text := range documents(t, seed) {
				doc := decodeDoubles(t, text)
				payload, _ := payloadOf(doc)
				valid := messageVerdict(schemas, doc) == nil
				if msg, err := ParseMessage(text); err == nil {
					if ev, ok := msg.Payload().(Event); ok && !reflect.DeepEqual(decodeDoubles(t, mustMarshal(t, ev)), payload) {
						t.Errorf("ParseMessage read the event %s from %s", mustMarshal(t, ev), text)
					}
				}
				if step, ok := NewReceiver().Receive(text).(Deliver); ok {
					delivered++
					if !valid || !reflect.DeepEqual(decodeDoubles(t, mustMarshal(t, step.Event)), payload) {
						t.Errorf("Receive delivered the event %s from %s, and the schema verdict is %v", mustMarshal(t, step.Event), text, valid)
					}
				}
			}
		}
		if delivered == 0 {
			t.Error("no document was delivered")
		}
	})
}
