package policy

import (
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const idPrefix = "urn:openplexer:spec:policy:v1:"

func decodeDoubles(t *testing.T, text []byte) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(text, &value); err != nil {
		t.Fatal(err)
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

func schemaProperty(t *testing.T, schema string, keys ...string) any {
	t.Helper()
	node := decodeDoubles(t, readFile(t, filepath.Join(specDir, schema+".schema.json")))
	for _, key := range keys {
		switch parent := node.(type) {
		case map[string]any:
			node = parent[key]
		case []any:
			var index int
			fmt.Sscan(key, &index)
			node = parent[index]
		}
	}
	return node
}

func TestTimestampPatternIsThePatternOfTheSchemas(t *testing.T) {
	places := map[string]any{
		"now of a query":                  schemaProperty(t, "eval-query", "properties", "now", "pattern"),
		"expires_at of a temporary grant": schemaProperty(t, "bundle", "properties", "temporary_grants", "items", "oneOf", "1", "properties", "expires_at", "pattern"),
	}
	for place, pattern := range places {
		if pattern != TimestampPattern {
			t.Errorf("the pattern of %s is %v, and TimestampPattern differs", place, pattern)
		}
	}
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
		if len(value) > 0 {
			out = append(out, slices.Repeat(value, 3), slices.Repeat(value, 201))
		}
	}
	return out
}

func TestParsersAgreeWithTheSchemas(t *testing.T) {
	schemas := compileSchemas(t)
	parsers := map[string]func([]byte) error{
		"bundle":     validators[idPrefix+"bundle"],
		"override":   validators[idPrefix+"override"],
		"eval-query": func(text []byte) error { _, err := ParseQuery(text); return err },
	}
	seeds := map[string][]any{}
	for name := range parsers {
		examples, err := filepath.Glob(filepath.Join(specDir, "examples", name, "*.json"))
		if err != nil || len(examples) == 0 {
			t.Fatalf("found no example of %s: %v", name, err)
		}
		for _, path := range examples {
			seeds[name] = append(seeds[name], decodeDoubles(t, readFile(t, path)))
		}
	}
	for _, path := range fixturePaths(t) {
		file := readFixture(t, path)
		switch file.Operation {
		case "validate":
			name := strings.TrimPrefix(file.Schema, idPrefix)
			for _, c := range file.Cases {
				seeds[name] = append(seeds[name], decodeDoubles(t, c.Input))
			}
		case "evaluate":
			seeds["bundle"] = append(seeds["bundle"], decodeDoubles(t, file.Shared.Bundle))
			for _, c := range file.Cases {
				seeds["eval-query"] = append(seeds["eval-query"], decodeDoubles(t, c.Input))
			}
		}
	}
	for name, parser := range parsers {
		compared, invalid := 0, 0
		for _, seed := range seeds[name] {
			for _, doc := range append([]any{seed}, variants(seed)...) {
				text, err := json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				schemaErr := schemas[name].Validate(doc)
				parseErr := parser(text)
				compared++
				if schemaErr != nil {
					invalid++
				}
				if (schemaErr == nil) != (parseErr == nil) {
					t.Errorf("%s: the schema says %v and the parser says %v for %s", name, schemaErr, parseErr, text)
				}
				if parseErr != nil {
					requireReasons(t, parseErr)
				}
			}
		}
		if invalid == 0 || invalid == compared {
			t.Errorf("%s: %d of %d documents are invalid, so the test compares one verdict only", name, invalid, compared)
		}
		t.Logf("%s: compared %d documents, %d of them invalid", name, compared, invalid)
	}
}

// spec/README.md, section Numbers: a reader holds every number as a double.
func TestVersionIsReadAsADouble(t *testing.T) {
	bundle := func(version string) []byte {
		return []byte(`{"version": ` + version + `, "project_id": "p", "repos": [], "areas": [], "roles": {}, "members": {}}`)
	}
	for version, valid := range map[string]bool{
		"1":                  true,
		"1e2":                true,
		"1.0000000000000001": true,
		"1.5":                false,
		"1e400":              false,
		"9007199254740993":   false,
	} {
		if _, err := ParseBundle(bundle(version)); (err == nil) != valid {
			t.Errorf("version %s: valid = %v, want %v (%v)", version, err == nil, valid, err)
		}
	}
}
