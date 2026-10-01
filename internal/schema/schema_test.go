package schema

import (
	"strings"
	"testing"
)

func mustCompile(t *testing.T, raw string) *Schema {
	t.Helper()
	compiled, err := Compile([]byte(raw))
	if err != nil {
		t.Fatalf("Compile(%s): %v", raw, err)
	}
	return compiled
}

func TestValidateObjectWithRequiredProperties(t *testing.T) {
	compiled := mustCompile(t, `{
		"type": "object",
		"properties": {
			"city": {"type": "string", "minLength": 1},
			"days": {"type": "integer", "minimum": 1, "maximum": 14}
		},
		"required": ["city"]
	}`)

	if errs := compiled.ValidateJSON([]byte(`{"city": "Paris"}`)); !errs.Empty() {
		t.Fatalf("valid value rejected: %v", errs)
	}
	if errs := compiled.ValidateJSON([]byte(`{"days": 3}`)); errs.Empty() {
		t.Error("a missing required property must fail")
	}
	if errs := compiled.ValidateJSON([]byte(`{"city": ""}`)); errs.Empty() {
		t.Error("an empty string must fail minLength")
	}
	if errs := compiled.ValidateJSON([]byte(`{"city": "Paris", "days": 99}`)); errs.Empty() {
		t.Error("a value above the maximum must fail")
	}
	if errs := compiled.ValidateJSON([]byte(`{"city": "Paris", "days": 3.5}`)); errs.Empty() {
		t.Error("a fractional value must fail an integer type")
	}
}

func TestErrorCarriesLocation(t *testing.T) {
	compiled := mustCompile(t, `{
		"type": "object",
		"properties": {"user": {"type": "object", "properties": {"age": {"type": "integer"}}}}
	}`)
	errs := compiled.ValidateJSON([]byte(`{"user": {"age": "old"}}`))
	if errs.Empty() {
		t.Fatal("expected a failure")
	}
	if message := errs.Error(); !strings.Contains(message, "/user/age") {
		t.Errorf("error should locate the field, got %q", message)
	}
}

func TestAdditionalPropertiesFalseRejectsExtras(t *testing.T) {
	compiled := mustCompile(t, `{
		"type": "object",
		"properties": {"a": {"type": "string"}},
		"additionalProperties": false
	}`)
	if errs := compiled.ValidateJSON([]byte(`{"a": "x"}`)); !errs.Empty() {
		t.Fatalf("declared property rejected: %v", errs)
	}
	if errs := compiled.ValidateJSON([]byte(`{"a": "x", "b": 1}`)); errs.Empty() {
		t.Error("an undeclared property must be rejected when additionalProperties is false")
	}
}

func TestEnumConstAndCombinators(t *testing.T) {
	compiled := mustCompile(t, `{
		"type": "object",
		"properties": {
			"unit": {"enum": ["celsius", "fahrenheit"]},
			"kind": {"const": "forecast"},
			"either": {"anyOf": [{"type": "string"}, {"type": "integer"}]},
			"exactly": {"oneOf": [{"type": "string"}, {"type": "integer"}]}
		}
	}`)
	body := `{"unit": "celsius", "kind": "forecast", "either": "x", "exactly": 3}`
	if errs := compiled.ValidateJSON([]byte(body)); !errs.Empty() {
		t.Fatalf("valid value rejected: %v", errs)
	}
	if errs := compiled.ValidateJSON([]byte(`{"unit": "kelvin"}`)); errs.Empty() {
		t.Error("a value outside the enum must fail")
	}
	if errs := compiled.ValidateJSON([]byte(`{"kind": "other"}`)); errs.Empty() {
		t.Error("a value differing from const must fail")
	}
	if errs := compiled.ValidateJSON([]byte(`{"either": true}`)); errs.Empty() {
		t.Error("anyOf must reject a type matching neither branch")
	}
}

func TestArraysAndLocalRefs(t *testing.T) {
	compiled := mustCompile(t, `{
		"$defs": {"stop": {"type": "string", "enum": ["a", "b"]}},
		"type": "object",
		"properties": {
			"stops": {"type": "array", "items": {"$ref": "#/$defs/stop"}, "minItems": 1},
			"pair": {"type": "array", "items": {"type": "integer"}, "minItems": 2, "maxItems": 2}
		}
	}`)
	if errs := compiled.ValidateJSON([]byte(`{"stops": ["a"], "pair": [1, 2]}`)); !errs.Empty() {
		t.Fatalf("valid value rejected: %v", errs)
	}
	if errs := compiled.ValidateJSON([]byte(`{"stops": ["c"]}`)); errs.Empty() {
		t.Error("an item outside the referenced enum must fail")
	}
	if errs := compiled.ValidateJSON([]byte(`{"stops": []}`)); errs.Empty() {
		t.Error("minItems must be enforced through the array schema")
	}
	if errs := compiled.ValidateJSON([]byte(`{"pair": [1, 2, 3]}`)); errs.Empty() {
		t.Error("maxItems must be enforced")
	}
}

func TestPatternIsCompiled(t *testing.T) {
	compiled := mustCompile(t, `{"type": "string", "pattern": "^[a-z]+$"}`)
	if errs := compiled.ValidateJSON([]byte(`"abc"`)); !errs.Empty() {
		t.Fatalf("matching string rejected: %v", errs)
	}
	if errs := compiled.ValidateJSON([]byte(`"ABC"`)); errs.Empty() {
		t.Error("a non-matching string must fail the pattern")
	}
}

func TestUnsupportedKeywordsRejectedAtCompileTime(t *testing.T) {
	// Silently ignoring these would make a schema pass validation while
	// enforcing nothing, which is the failure this package exists to prevent.
	for _, raw := range []string{
		`{"type": "object", "$dynamicRef": "#node"}`,
		`{"type": "object", "unevaluatedProperties": false}`,
		`{"type": "object", "dependentRequired": {"a": ["b"]}}`,
		`{"$ref": "https://example.com/schema.json"}`,
	} {
		if _, err := Compile([]byte(raw)); err == nil {
			t.Errorf("expected a compile error for %s", raw)
		}
	}
}

func TestMalformedSchemasFailToCompile(t *testing.T) {
	for _, raw := range []string{`not json`, `[]`, `"a string"`, `{"type": 5}`} {
		if _, err := Compile([]byte(raw)); err == nil {
			t.Errorf("expected a compile error for %s", raw)
		}
	}
}

func TestEmptySchemaAcceptsAnything(t *testing.T) {
	compiled := mustCompile(t, `{}`)
	if !compiled.IsEmpty() {
		t.Error("an empty schema must report itself as unconstrained")
	}
	for _, raw := range []string{`{}`, `{"a": 1}`, `[1,2]`, `"text"`, `null`} {
		if errs := compiled.ValidateJSON([]byte(raw)); !errs.Empty() {
			t.Errorf("an empty schema rejected %s: %v", raw, errs)
		}
	}
}

func TestUniqueItemsAndMultipleOf(t *testing.T) {
	compiled := mustCompile(t, `{
		"type": "object",
		"properties": {
			"tags": {"type": "array", "uniqueItems": true},
			"step": {"type": "number", "multipleOf": 0.5}
		}
	}`)
	if errs := compiled.ValidateJSON([]byte(`{"tags": ["a", "b"], "step": 1.5}`)); !errs.Empty() {
		t.Fatalf("valid value rejected: %v", errs)
	}
	if errs := compiled.ValidateJSON([]byte(`{"tags": ["a", "a"]}`)); errs.Empty() {
		t.Error("duplicate items must fail uniqueItems")
	}
	if errs := compiled.ValidateJSON([]byte(`{"step": 1.2}`)); errs.Empty() {
		t.Error("a value that is not a multiple must fail")
	}
}

func TestUnknownKeywordIsRejected(t *testing.T) {
	// A keyword this validator does not implement must be an error, not a
	// silent pass. Each of these would otherwise be a constraint that reads as
	// enforced and permits anything, which is worse than no schema at all.
	for _, raw := range []string{
		`{"if": {"required": ["a"]}, "then": {"required": ["b"]}}`,
		`{"prefixItems": [{"type": "string"}]}`,
		`{"patternProperties": {"^x": {"type": "string"}}}`,
		`{"propertyNames": {"minLength": 1}}`,
		`{"$dynamicRef": "#node"}`,
		`{"unevaluatedProperties": false}`,
		`{"contains": {"type": "string"}}`,
		`{"dependentRequired": {"a": ["b"]}}`,
		`{"minProperties": 1}`,
	} {
		if _, err := Compile([]byte(raw)); err == nil {
			t.Errorf("Compile(%s) must fail: an unimplemented keyword enforces nothing", raw)
		} else if !strings.Contains(err.Error(), "not supported") {
			t.Errorf("Compile(%s) error should say the keyword is unsupported: %v", raw, err)
		}
	}
}

func TestUnknownNestedKeywordIsRejected(t *testing.T) {
	// The allow-list has to be applied to every nested schema, not just the
	// root, or a keyword can be hidden one level down.
	for _, raw := range []string{
		`{"type": "object", "properties": {"a": {"if": {}}}}`,
		`{"type": "object", "additionalProperties": {"prefixItems": []}}`,
		`{"type": "array", "items": {"$dynamicRef": "#x"}}`,
		`{"allOf": [{"not": {"propertyNames": {}}}]}`,
		`{"$defs": {"Inner": {"contains": {}}}}`,
		`{"definitions": {"Inner": {"unevaluatedItems": false}}}`,
	} {
		if _, err := Compile([]byte(raw)); err == nil {
			t.Errorf("Compile(%s) must fail: a nested unimplemented keyword enforces nothing", raw)
		}
	}
}

func TestAnnotationKeywordsAreAccepted(t *testing.T) {
	// Annotations carry no assertion, so accepting them changes no verdict.
	// Rejecting them would break otherwise-valid schemas that carry
	// documentation alongside their constraints.
	compiled := mustCompile(t, `{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$id": "https://example.com/tool.json",
		"title": "Status",
		"description": "A status report",
		"default": {"ok": true},
		"examples": [{"ok": true}],
		"deprecated": false,
		"readOnly": false,
		"format": "email",
		"type": "object",
		"properties": {"ok": {"type": "boolean", "description": "whether it worked"}}
	}`)
	if errs := compiled.ValidateJSON([]byte(`{"ok": true}`)); !errs.Empty() {
		t.Errorf("annotations must not change validation: %v", errs)
	}
	if errs := compiled.ValidateJSON([]byte(`{"ok": "yes"}`)); errs.Empty() {
		t.Error("the real constraint must still be enforced")
	}
}

func TestKnownTypeNamesAreCheckedAtCompileTime(t *testing.T) {
	// A type name that is not a JSON type enforces nothing, and finding out
	// only when a value fails is the worst possible time to learn it.
	if _, err := Compile([]byte(`{"type": "object"}`)); err != nil {
		t.Fatalf("a valid type rejected: %v", err)
	}
	for _, raw := range []string{
		`{"type": "objcet"}`,
		`{"properties": {"a": {"type": "integerr"}}}`,
		`{"type": 42}`,
		`{"type": ["string", 7]}`,
	} {
		if _, err := Compile([]byte(raw)); err == nil {
			t.Errorf("Compile(%s) must fail on an invalid type name", raw)
		}
	}
}
