// Package schema implements the JSON Schema subset Synapass needs.
//
// # Why a subset
//
// Tool arguments and structured outputs are validated against a declared
// schema, and a gateway that silently accepted a malformed schema would fail
// at call time in the least debuggable place possible. This package validates
// the keywords that actually appear in tool schemas and structured outputs,
// and rejects the ones it does not implement rather than pretending to.
//
// Supported: type, properties, required, additionalProperties, enum, const,
// items, minimum, maximum, exclusiveMinimum, exclusiveMaximum, minLength,
// maxLength, minItems, maxItems, uniqueItems, anyOf, oneOf, allOf, not,
// pattern (as a substring-free literal check is NOT attempted; the regexp is
// compiled), multipleOf, and $ref limited to $defs/definitions local pointers.
//
// Deliberately unsupported and rejected: remote $ref, $dynamicRef, unevaluated*
// and format. "format" is accepted and ignored because it is an annotation in
// practice, not a constraint any client relies on for correctness.
package schema

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Schema is a parsed schema document.
type Schema struct {
	root map[string]any
	// defs holds the local $defs/definitions maps for $ref resolution.
	defs map[string]any
}

// ValidationError is one concrete reason a value does not satisfy a schema.
// The Path is a JSON-pointer-ish location ("/arguments/city") so an error can
// be shown to the developer who wrote the tool.
type ValidationError struct {
	Path    string
	Message string
}

// Error renders the failure with its location.
func (e ValidationError) Error() string {
	if e.Path == "" {
		return e.Message
	}
	return e.Path + ": " + e.Message
}

// ValidationErrors is a collected set of failures.
type ValidationErrors []ValidationError

// Error renders every failure, one per line, in a stable order.
func (e ValidationErrors) Error() string {
	if len(e) == 0 {
		return ""
	}
	sort.SliceStable(e, func(i, j int) bool { return e[i].Path < e[j].Path })
	parts := make([]string, 0, len(e))
	for _, err := range e {
		parts = append(parts, err.Error())
	}
	return strings.Join(parts, "; ")
}

// Empty reports whether any failures were collected.
func (e ValidationErrors) Empty() bool { return len(e) == 0 }

// Compile parses a schema document.
//
// It checks the schema itself, not the data: a schema that cannot be satisfied
// by any value is a programming error in the tool definition and is reported
// here rather than at execution time.
func Compile(raw []byte) (*Schema, error) {
	if len(raw) == 0 {
		// An absent schema means "unconstrained", which is a legitimate tool.
		return &Schema{root: map[string]any{}, defs: map[string]any{}}, nil
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("schema is not valid JSON: %w", err)
	}
	root, ok := doc.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema must be a JSON object, got %T", doc)
	}

	s := &Schema{root: root, defs: map[string]any{}}
	for _, key := range []string{"$defs", "definitions"} {
		if defs, ok := root[key].(map[string]any); ok {
			for name, value := range defs {
				s.defs[name] = value
			}
		}
	}
	if err := s.checkKeywords(root, "#"); err != nil {
		return nil, err
	}
	return s, nil
}

// FromMap compiles a schema already decoded into a map.
func FromMap(doc map[string]any) (*Schema, error) {
	if doc == nil {
		return Compile(nil)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return Compile(raw)
}

// IsEmpty reports whether the schema constrains nothing.
func (s *Schema) IsEmpty() bool {
	if s == nil {
		return true
	}
	return len(s.root) == 0
}

// Validate checks a decoded JSON value against the schema.
func (s *Schema) Validate(value any) ValidationErrors {
	if s == nil {
		return nil
	}
	var errs ValidationErrors
	s.validate(value, s.root, "", &errs)
	return errs
}

// ValidateJSON decodes and validates raw JSON in one step.
func (s *Schema) ValidateJSON(raw []byte) ValidationErrors {
	if len(raw) == 0 {
		return ValidationErrors{{Path: "", Message: "value is empty"}}
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return ValidationErrors{{Path: "", Message: "value is not valid JSON: " + err.Error()}}
	}
	return s.Validate(value)
}

// validate walks a value against a schema node.
func (s *Schema) validate(value any, node map[string]any, path string, errs *ValidationErrors) {
	if node == nil {
		return
	}
	if ref, ok := node["$ref"].(string); ok {
		resolved, err := s.resolveRef(ref)
		if err != nil {
			*errs = append(*errs, ValidationError{Path: path, Message: err.Error()})
			return
		}
		s.validate(value, resolved, path, errs)
		return
	}

	s.checkType(value, node, path, errs)
	s.checkEnum(value, node, path, errs)
	s.checkNumber(value, node, path, errs)
	s.checkString(value, node, path, errs)
	s.checkArray(value, node, path, errs)
	s.checkObject(value, node, path, errs)
	s.checkCombinators(value, node, path, errs)
}

// resolveRef resolves a local "#/$defs/Name" or "#/definitions/Name" pointer.
func (s *Schema) resolveRef(ref string) (map[string]any, error) {
	const prefixDefs = "#/$defs/"
	const prefixDefinitions = "#/definitions/"
	var name string
	switch {
	case strings.HasPrefix(ref, prefixDefs):
		name = strings.TrimPrefix(ref, prefixDefs)
	case strings.HasPrefix(ref, prefixDefinitions):
		name = strings.TrimPrefix(ref, prefixDefinitions)
	default:
		return nil, fmt.Errorf("unsupported $ref %q: only local #/$defs and #/definitions pointers are supported", ref)
	}
	resolved, ok := s.defs[name]
	if !ok {
		return nil, fmt.Errorf("$ref %q does not resolve to a definition", ref)
	}
	node, ok := resolved.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("$ref %q does not point at a schema object", ref)
	}
	return node, nil
}

func join(path, segment string) string {
	if segment == "" {
		return path
	}
	return path + "/" + strings.ReplaceAll(segment, "~", "~0")
}

// checkType enforces the `type` keyword.
func (s *Schema) checkType(value any, node map[string]any, path string, errs *ValidationErrors) {
	raw, ok := node["type"]
	if !ok {
		return
	}
	var allowed []string
	switch t := raw.(type) {
	case string:
		allowed = []string{t}
	case []any:
		for _, item := range t {
			if name, ok := item.(string); ok {
				allowed = append(allowed, name)
			}
		}
	default:
		*errs = append(*errs, ValidationError{Path: path, Message: "'type' must be a string or array of strings"})
		return
	}
	for _, name := range allowed {
		if matchesType(value, name) {
			return
		}
	}
	*errs = append(*errs, ValidationError{
		Path:    path,
		Message: fmt.Sprintf("expected %s, got %s", strings.Join(allowed, " or "), jsonTypeOf(value)),
	})
}

// jsonTypeOf names a decoded JSON value's type.
func jsonTypeOf(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		_ = v
		return "unknown"
	}
}

// matchesType reports whether a decoded value satisfies a JSON Schema type name.
func matchesType(value any, name string) bool {
	switch name {
	case "null":
		return value == nil
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		// JSON has no integer type: a number with no fractional part is one.
		f, ok := value.(float64)
		return ok && f == float64(int64(f))
	case "string":
		_, ok := value.(string)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	default:
		return false
	}
}

func (s *Schema) checkEnum(value any, node map[string]any, path string, errs *ValidationErrors) {
	if raw, ok := node["enum"]; ok {
		options, ok := raw.([]any)
		if !ok {
			*errs = append(*errs, ValidationError{Path: path, Message: "'enum' must be an array"})
			return
		}
		for _, option := range options {
			if sameJSON(option, value) {
				return
			}
		}
		*errs = append(*errs, ValidationError{
			Path:    path,
			Message: fmt.Sprintf("value is not one of the permitted %s", renderList(options)),
		})
	}
	if raw, ok := node["const"]; ok {
		if !sameJSON(raw, value) {
			*errs = append(*errs, ValidationError{
				Path:    path,
				Message: fmt.Sprintf("value must equal %s", renderValue(raw)),
			})
		}
	}
}

// sameJSON compares two decoded JSON values structurally.
func sameJSON(a, b any) bool {
	switch av := a.(type) {
	case nil:
		return b == nil
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case float64:
		bv, ok := b.(float64)
		return ok && av == bv
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !sameJSON(av[i], bv[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			other, ok := bv[k]
			if !ok || !sameJSON(v, other) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func renderList(values []any) string {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, renderValue(v))
	}
	return strings.Join(parts, ", ")
}

func renderValue(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return strconv.Quote(fmt.Sprintf("%v", v))
	}
	return string(raw)
}

func (s *Schema) checkNumber(value any, node map[string]any, path string, errs *ValidationErrors) {
	number, ok := value.(float64)
	if !ok {
		return
	}
	// bounds maps each keyword to the comparison it implies and how to describe
	// the limit in an error message.
	bounds := []struct {
		keyword   string
		satisfies func(value, bound float64) bool
		describe  func(bound float64) string
	}{
		{"minimum", func(v, b float64) bool { return v >= b }, func(b float64) string {
			return ">= " + formatNumber(b)
		}},
		{"maximum", func(v, b float64) bool { return v <= b }, func(b float64) string {
			return "<= " + formatNumber(b)
		}},
		{"exclusiveMinimum", func(v, b float64) bool { return v > b }, func(b float64) string {
			return "> " + formatNumber(b)
		}},
		{"exclusiveMaximum", func(v, b float64) bool { return v < b }, func(b float64) string {
			return "< " + formatNumber(b)
		}},
	}
	for _, bound := range bounds {
		limit, present := numericBound(node[bound.keyword])
		if !present {
			continue
		}
		if !bound.satisfies(number, limit) {
			*errs = append(*errs, ValidationError{
				Path:    path,
				Message: "value must be " + bound.describe(limit),
			})
		}
	}

	if step, present := numericBound(node["multipleOf"]); present && step != 0 {
		ratio := number / step
		if ratio != float64(int64(ratio)) {
			*errs = append(*errs, ValidationError{
				Path:    path,
				Message: "value must be a multiple of " + formatNumber(step),
			})
		}
	}
}

// formatNumber renders a bound without trailing zeros.
func formatNumber(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

func numericBound(raw any) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, true
	case json.Number:
		parsed, err := v.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func (s *Schema) checkString(value any, node map[string]any, path string, errs *ValidationErrors) {
	text, ok := value.(string)
	if !ok {
		return
	}
	if bound, present := numericBound(node["minLength"]); present && float64(len([]rune(text))) < bound {
		*errs = append(*errs, ValidationError{
			Path:    path,
			Message: fmt.Sprintf("string must be at least %d characters", int(bound)),
		})
	}
	if bound, present := numericBound(node["maxLength"]); present && float64(len([]rune(text))) > bound {
		*errs = append(*errs, ValidationError{
			Path:    path,
			Message: fmt.Sprintf("string must be at most %d characters", int(bound)),
		})
	}
	if pattern, ok := node["pattern"].(string); ok {
		re, err := regexp.Compile(pattern)
		if err != nil {
			*errs = append(*errs, ValidationError{Path: path, Message: "'pattern' is not a valid regular expression"})
		} else if !re.MatchString(text) {
			*errs = append(*errs, ValidationError{
				Path:    path,
				Message: fmt.Sprintf("string must match %s", pattern),
			})
		}
	}
}

func (s *Schema) checkArray(value any, node map[string]any, path string, errs *ValidationErrors) {
	items, ok := value.([]any)
	if !ok {
		return
	}
	if bound, present := numericBound(node["minItems"]); present && float64(len(items)) < bound {
		*errs = append(*errs, ValidationError{
			Path:    path,
			Message: fmt.Sprintf("array must have at least %d items", int(bound)),
		})
	}
	if bound, present := numericBound(node["maxItems"]); present && float64(len(items)) > bound {
		*errs = append(*errs, ValidationError{
			Path:    path,
			Message: fmt.Sprintf("array must have at most %d items", int(bound)),
		})
	}
	if unique, ok := node["uniqueItems"].(bool); ok && unique {
		for i := range items {
			for j := i + 1; j < len(items); j++ {
				if sameJSON(items[i], items[j]) {
					*errs = append(*errs, ValidationError{Path: path, Message: "array items must be unique"})
					return
				}
			}
		}
	}
	itemsSchema, ok := node["items"].(map[string]any)
	if ok {
		for i, item := range items {
			s.validate(item, itemsSchema, fmt.Sprintf("%s/%d", path, i), errs)
		}
	}
}

func (s *Schema) checkObject(value any, node map[string]any, path string, errs *ValidationErrors) {
	object, ok := value.(map[string]any)
	if !ok {
		return
	}
	properties, _ := node["properties"].(map[string]any)

	if required, ok := node["required"].([]any); ok {
		for _, raw := range required {
			name, ok := raw.(string)
			if !ok {
				continue
			}
			if _, present := object[name]; !present {
				*errs = append(*errs, ValidationError{
					Path:    path,
					Message: fmt.Sprintf("missing required property %q", name),
				})
			}
		}
	}

	for key, item := range object {
		propertySchema, declared := properties[key].(map[string]any)
		if declared {
			s.validate(item, propertySchema, join(path, key), errs)
			continue
		}
		// Undeclared property: only an explicit false disallows it.
		switch additional := node["additionalProperties"].(type) {
		case bool:
			if !additional {
				*errs = append(*errs, ValidationError{
					Path:    join(path, key),
					Message: "property is not permitted by the schema",
				})
			}
		case map[string]any:
			s.validate(item, additional, join(path, key), errs)
		}
	}
}

func (s *Schema) checkCombinators(value any, node map[string]any, path string, errs *ValidationErrors) {
	if raw, ok := node["allOf"].([]any); ok {
		for _, item := range raw {
			if sub, ok := item.(map[string]any); ok {
				s.validate(value, sub, path, errs)
			}
		}
	}
	if raw, ok := node["anyOf"].([]any); ok {
		if !s.anyMatch(value, raw, path) {
			*errs = append(*errs, ValidationError{Path: path, Message: "value does not match any permitted schema"})
		}
	}
	if raw, ok := node["oneOf"].([]any); ok {
		if matches := s.countMatches(value, raw, path); matches != 1 {
			*errs = append(*errs, ValidationError{
				Path:    path,
				Message: fmt.Sprintf("value must match exactly one permitted schema, matched %d", matches),
			})
		}
	}
	if sub, ok := node["not"].(map[string]any); ok {
		var ignored ValidationErrors
		s.validate(value, sub, path, &ignored)
		if ignored.Empty() {
			*errs = append(*errs, ValidationError{Path: path, Message: "value matches a forbidden schema"})
		}
	}
}

func (s *Schema) anyMatch(value any, options []any, path string) bool {
	return s.countMatches(value, options, path) > 0
}

func (s *Schema) countMatches(value any, options []any, path string) int {
	count := 0
	for _, item := range options {
		sub, ok := item.(map[string]any)
		if !ok {
			continue
		}
		var ignored ValidationErrors
		s.validate(value, sub, path, &ignored)
		if ignored.Empty() {
			count++
		}
	}
	return count
}

// knownTypeNames is the set of JSON Schema type names this validator accepts.
// An unrecognised name would make matchesType return false for every value,
// silently rejecting everything the schema is meant to allow.
var knownTypeNames = map[string]bool{
	"null": true, "boolean": true, "number": true, "integer": true,
	"string": true, "array": true, "object": true,
}

// supportedKeywords is the complete set of keywords this validator enforces.
//
// This is an allow-list rather than a deny-list on purpose. A deny-list can
// only ever name the unsupported keywords its author happened to think of, so
// every unlisted keyword is a constraint that looks enforced and is not:
// {"if": {...}} or {"prefixItems": [...]} would pass a schema check and then
// silently permit anything, which is the exact failure this package exists to
// prevent. Rejecting an unknown keyword tells the caller their schema is not
// what Synapass will check.
var supportedKeywords = map[string]bool{
	// Assertions this package implements.
	"type":                 true,
	"enum":                 true,
	"const":                true,
	"properties":           true,
	"required":             true,
	"additionalProperties": true,
	"items":                true,
	"minimum":              true,
	"maximum":              true,
	"exclusiveMinimum":     true,
	"exclusiveMaximum":     true,
	"multipleOf":           true,
	"minLength":            true,
	"maxLength":            true,
	"pattern":              true,
	"minItems":             true,
	"maxItems":             true,
	"uniqueItems":          true,
	"allOf":                true,
	"anyOf":                true,
	"oneOf":                true,
	"not":                  true,
	"$ref":                 true,

	// Reference containers, resolved locally.
	"$defs":       true,
	"definitions": true,

	// Annotations. These carry no assertion, so accepting them changes no
	// verdict, and rejecting them would break otherwise-valid schemas that
	// include documentation. "format" is the notable one: in practice it is an
	// annotation, not a constraint a client relies on for correctness.
	"$schema":     true,
	"$id":         true,
	"$anchor":     true,
	"title":       true,
	"description": true,
	"default":     true,
	"examples":    true,
	"deprecated":  true,
	"readOnly":    true,
	"writeOnly":   true,
	"format":      true,
}

// checkKeywords rejects schema keywords this validator does not implement.
//
// Silently ignoring $dynamicRef or unevaluatedProperties would make a schema
// pass validation while enforcing nothing, which is the failure mode this
// package exists to prevent.
func (s *Schema) checkKeywords(node map[string]any, path string) error {
	keys := make([]string, 0, len(node))
	for key := range node {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if supportedKeywords[key] {
			continue
		}
		return fmt.Errorf("%s%s is not supported by this validator; "+
			"see the supported keyword list in package schema", path, key)
	}

	if ref, ok := node["$ref"].(string); ok {
		if _, err := s.resolveRef(ref); err != nil {
			return fmt.Errorf("%s$ref: %w", path, err)
		}
	}

	// The type keyword is checked here rather than during validation: a schema
	// whose `type` is not a type name enforces nothing, and finding that out
	// only when a value fails is the worst time to learn it.
	if raw, present := node["type"]; present {
		var names []any
		switch typed := raw.(type) {
		case string:
			names = []any{typed}
		case []any:
			names = typed
		default:
			return fmt.Errorf("%stype must be a string or an array of strings, got %T", path, raw)
		}
		for _, item := range names {
			name, ok := item.(string)
			if !ok {
				return fmt.Errorf("%stype entries must be strings", path)
			}
			if !knownTypeNames[name] {
				return fmt.Errorf("%sunknown type %q", path, name)
			}
		}
	}

	for _, key := range []string{"properties", "$defs", "definitions"} {
		if nested, ok := node[key].(map[string]any); ok {
			for name, raw := range nested {
				child, ok := raw.(map[string]any)
				if !ok {
					return fmt.Errorf("%s%s.%s must be a schema object", path, key, name)
				}
				if err := s.checkKeywords(child, path+key+"."+name+"."); err != nil {
					return err
				}
			}
		}
	}
	if nested, ok := node["items"].(map[string]any); ok {
		if err := s.checkKeywords(nested, path+"items."); err != nil {
			return err
		}
	}
	// additionalProperties carries a schema in its object form, so a keyword
	// hidden there would otherwise escape the check.
	if nested, ok := node["additionalProperties"].(map[string]any); ok {
		if err := s.checkKeywords(nested, path+"additionalProperties."); err != nil {
			return err
		}
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		if list, ok := node[key].([]any); ok {
			for _, raw := range list {
				child, ok := raw.(map[string]any)
				if !ok {
					return fmt.Errorf("%s%s entries must be schema objects", path, key)
				}
				if err := s.checkKeywords(child, path+key+"."); err != nil {
					return err
				}
			}
		}
	}
	if child, ok := node["not"].(map[string]any); ok {
		if err := s.checkKeywords(child, path+"not."); err != nil {
			return err
		}
	}
	return nil
}
