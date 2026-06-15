// SPDX-License-Identifier: AGPL-3.0-or-later

// Package schema validates JSON documents against a supported subset of JSON
// Schema. It is shared by the decision engine (decide-input contracts) and the
// Agent Manager (agent structured output).
//
// Supported keywords: type (string or array), required, properties,
// additionalProperties (bool or schema), minProperties/maxProperties, enum,
// const, the combinators allOf/anyOf/oneOf/not, numeric minimum/maximum/
// exclusiveMinimum/exclusiveMaximum/multipleOf, string minLength/maxLength/
// pattern and a small set of formats (email, date-time, date, uuid), array
// items/minItems/maxItems/uniqueItems, and local $ref (a JSON pointer into the
// same document, e.g. "#/$defs/Name"). Schema nodes may be objects or the
// booleans true/false. Unknown keywords are ignored (lenient), so a document is
// never rejected for a keyword this validator does not implement.
package schema

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// maxDepth bounds recursion to fail loudly on a pathologically nested or
// $ref-cyclic schema instead of overflowing the stack.
const maxDepth = 128

// ValidateObject checks data against schema. An empty schema is no contract. A
// schema that is present but not a JSON object (or boolean) is a broken contract
// and fails loudly. Otherwise it enforces the declared keywords.
func ValidateObject(schema json.RawMessage, data map[string]any) error {
	if len(schema) == 0 {
		return nil
	}
	var node any
	if err := json.Unmarshal(schema, &node); err != nil {
		return fmt.Errorf("schema: not a valid schema document: %w", err)
	}
	switch node.(type) {
	case map[string]any, bool:
		// ok — a schema is an object or a boolean
	default:
		return fmt.Errorf("schema: a schema must be a JSON object or boolean, got %T", node)
	}
	v := &validator{root: node}
	return v.validate(node, data, "(root)", 0)
}

type validator struct {
	root any
}

// validate checks value against the schema node at path. It returns the first
// violation it finds.
func (v *validator) validate(node, value any, path string, depth int) error {
	if depth > maxDepth {
		return fmt.Errorf("schema: %s: nesting/$ref depth exceeded %d", path, maxDepth)
	}
	switch s := node.(type) {
	case bool:
		if !s {
			return fmt.Errorf("schema: %s: schema is false (no value is valid)", path)
		}
		return nil
	case map[string]any:
		return v.validateObjectNode(s, value, path, depth)
	default:
		return fmt.Errorf("schema: %s: invalid sub-schema %T", path, node)
	}
}

func (v *validator) validateObjectNode(s map[string]any, value any, path string, depth int) error {
	if ref, ok := s["$ref"].(string); ok {
		target, err := v.resolveRef(ref)
		if err != nil {
			return fmt.Errorf("schema: %s: %w", path, err)
		}
		if err := v.validate(target, value, path+"->"+ref, depth+1); err != nil {
			return err
		}
	}
	for _, check := range []func(map[string]any, any, string, int) error{
		v.checkType, v.checkEnum, v.checkConst, v.checkCombinators,
		v.checkNumber, v.checkString, v.checkArray, v.checkObject,
	} {
		if err := check(s, value, path, depth); err != nil {
			return err
		}
	}
	return nil
}

func (v *validator) checkType(s map[string]any, value any, path string, _ int) error {
	raw, ok := s["type"]
	if !ok {
		return nil
	}
	var types []string
	switch t := raw.(type) {
	case string:
		types = []string{t}
	case []any:
		for _, e := range t {
			if str, ok := e.(string); ok {
				types = append(types, str)
			}
		}
	}
	for _, t := range types {
		if typeMatches(t, value) {
			return nil
		}
	}
	if len(types) == 0 {
		return nil
	}
	return fmt.Errorf("schema: %s: must be %s", path, strings.Join(types, " or "))
}

func (v *validator) checkEnum(s map[string]any, value any, path string, _ int) error {
	raw, ok := s["enum"]
	if !ok {
		return nil
	}
	options, ok := raw.([]any)
	if !ok {
		return nil
	}
	for _, opt := range options {
		if jsonEqual(opt, value) {
			return nil
		}
	}
	return fmt.Errorf("schema: %s: must be one of the enumerated values", path)
}

func (v *validator) checkConst(s map[string]any, value any, path string, _ int) error {
	want, ok := s["const"]
	if !ok {
		return nil
	}
	if !jsonEqual(want, value) {
		return fmt.Errorf("schema: %s: must equal the const value", path)
	}
	return nil
}

func (v *validator) checkCombinators(s map[string]any, value any, path string, depth int) error {
	if sub, ok := s["allOf"].([]any); ok {
		for i, n := range sub {
			if err := v.validate(n, value, fmt.Sprintf("%s/allOf[%d]", path, i), depth+1); err != nil {
				return err
			}
		}
	}
	if sub, ok := s["anyOf"].([]any); ok {
		matched := false
		for _, n := range sub {
			if v.validate(n, value, path, depth+1) == nil {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("schema: %s: matched none of anyOf", path)
		}
	}
	if sub, ok := s["oneOf"].([]any); ok {
		count := 0
		for _, n := range sub {
			if v.validate(n, value, path, depth+1) == nil {
				count++
			}
		}
		if count != 1 {
			return fmt.Errorf("schema: %s: must match exactly one of oneOf (matched %d)", path, count)
		}
	}
	if n, ok := s["not"]; ok {
		if v.validate(n, value, path, depth+1) == nil {
			return fmt.Errorf("schema: %s: must not match the 'not' schema", path)
		}
	}
	return nil
}

func (v *validator) checkNumber(s map[string]any, value any, path string, _ int) error {
	f, ok := value.(float64)
	if !ok {
		return nil // not a number; type check (if any) handles mismatch
	}
	if lo, ok := s["minimum"].(float64); ok && f < lo {
		return fmt.Errorf("schema: %s: must be >= %v", path, lo)
	}
	if hi, ok := s["maximum"].(float64); ok && f > hi {
		return fmt.Errorf("schema: %s: must be <= %v", path, hi)
	}
	if ex, ok := s["exclusiveMinimum"].(float64); ok && f <= ex {
		return fmt.Errorf("schema: %s: must be > %v", path, ex)
	}
	if ex, ok := s["exclusiveMaximum"].(float64); ok && f >= ex {
		return fmt.Errorf("schema: %s: must be < %v", path, ex)
	}
	if mult, ok := s["multipleOf"].(float64); ok && mult > 0 {
		if r := f / mult; math.Abs(r-math.Round(r)) > 1e-9 {
			return fmt.Errorf("schema: %s: must be a multiple of %v", path, mult)
		}
	}
	return nil
}

func (v *validator) checkString(s map[string]any, value any, path string, _ int) error {
	str, ok := value.(string)
	if !ok {
		return nil
	}
	if lo, ok := s["minLength"].(float64); ok && float64(len([]rune(str))) < lo {
		return fmt.Errorf("schema: %s: must be at least %v characters", path, lo)
	}
	if hi, ok := s["maxLength"].(float64); ok && float64(len([]rune(str))) > hi {
		return fmt.Errorf("schema: %s: must be at most %v characters", path, hi)
	}
	if pat, ok := s["pattern"].(string); ok {
		re, err := regexp.Compile(pat)
		if err != nil {
			return fmt.Errorf("schema: %s: invalid pattern %q: %w", path, pat, err)
		}
		if !re.MatchString(str) {
			return fmt.Errorf("schema: %s: must match pattern %q", path, pat)
		}
	}
	if f, ok := s["format"].(string); ok && !formatMatches(f, str) {
		return fmt.Errorf("schema: %s: must be a valid %s", path, f)
	}
	return nil
}

func (v *validator) checkArray(s map[string]any, value any, path string, depth int) error {
	arr, ok := value.([]any)
	if !ok {
		return nil
	}
	if lo, ok := s["minItems"].(float64); ok && float64(len(arr)) < lo {
		return fmt.Errorf("schema: %s: must have at least %v items", path, lo)
	}
	if hi, ok := s["maxItems"].(float64); ok && float64(len(arr)) > hi {
		return fmt.Errorf("schema: %s: must have at most %v items", path, hi)
	}
	if uniq, ok := s["uniqueItems"].(bool); ok && uniq {
		for i := 0; i < len(arr); i++ {
			for j := i + 1; j < len(arr); j++ {
				if jsonEqual(arr[i], arr[j]) {
					return fmt.Errorf("schema: %s: items must be unique (duplicate at %d,%d)", path, i, j)
				}
			}
		}
	}
	if items, ok := s["items"]; ok {
		for i, e := range arr {
			if err := v.validate(items, e, fmt.Sprintf("%s[%d]", path, i), depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func (v *validator) checkObject(s map[string]any, value any, path string, depth int) error {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if req, ok := s["required"].([]any); ok {
		for _, r := range req {
			name, ok := r.(string)
			if !ok {
				continue
			}
			if _, present := obj[name]; !present {
				return fmt.Errorf("schema: %s: missing required field %q", path, name)
			}
		}
	}
	if lo, ok := s["minProperties"].(float64); ok && float64(len(obj)) < lo {
		return fmt.Errorf("schema: %s: must have at least %v properties", path, lo)
	}
	if hi, ok := s["maxProperties"].(float64); ok && float64(len(obj)) > hi {
		return fmt.Errorf("schema: %s: must have at most %v properties", path, hi)
	}
	props, _ := s["properties"].(map[string]any)
	// Validate declared properties in a stable order for deterministic errors.
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		val, present := obj[name]
		if !present {
			continue
		}
		if err := v.validate(props[name], val, path+"."+name, depth+1); err != nil {
			return err
		}
	}
	return v.checkAdditionalProperties(s, obj, props, path, depth)
}

func (v *validator) checkAdditionalProperties(s, obj, props map[string]any, path string, depth int) error {
	ap, ok := s["additionalProperties"]
	if !ok {
		return nil
	}
	extra := func(name string) bool { _, declared := props[name]; return !declared }
	switch a := ap.(type) {
	case bool:
		if a {
			return nil
		}
		for _, name := range sortedKeys(obj) {
			if extra(name) {
				return fmt.Errorf("schema: %s: additional property %q is not allowed", path, name)
			}
		}
	case map[string]any, []any:
		for _, name := range sortedKeys(obj) {
			if extra(name) {
				if err := v.validate(ap, obj[name], path+"."+name, depth+1); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// resolveRef resolves a local JSON pointer ($ref) against the root schema.
func (v *validator) resolveRef(ref string) (any, error) {
	if ref == "#" || ref == "" {
		return v.root, nil
	}
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("only local $ref (\"#/...\") is supported, got %q", ref)
	}
	cur := v.root
	for _, token := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[token]
			if !ok {
				return nil, fmt.Errorf("$ref %q: no key %q", ref, token)
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(token)
			if err != nil || i < 0 || i >= len(node) {
				return nil, fmt.Errorf("$ref %q: bad index %q", ref, token)
			}
			cur = node[i]
		default:
			return nil, fmt.Errorf("$ref %q: cannot descend into %T at %q", ref, cur, token)
		}
	}
	return cur, nil
}

// typeMatches reports whether v (decoded from JSON into Go's any) satisfies the
// JSON Schema type t. Unknown types are accepted (lenient).
func typeMatches(t string, v any) bool {
	switch t {
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		f, ok := v.(float64)
		return ok && f == math.Trunc(f)
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "null":
		return v == nil
	default:
		return true
	}
}

// formatMatches enforces a small set of common formats; unknown formats are
// accepted (JSON Schema treats format as an annotation by default).
func formatMatches(format, s string) bool {
	switch format {
	case "email":
		return reEmail.MatchString(s)
	case "uuid":
		return reUUID.MatchString(s)
	case "date-time":
		return reDateTime.MatchString(s)
	case "date":
		return reDate.MatchString(s)
	default:
		return true
	}
}

var (
	reEmail    = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	reUUID     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	reDate     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	reDateTime = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[Tt]\d{2}:\d{2}:\d{2}`)
)

// jsonEqual compares two JSON-decoded values for deep equality. Both sides come
// from encoding/json, so numbers are float64 on both — reflect.DeepEqual is exact.
func jsonEqual(a, b any) bool {
	return reflect.DeepEqual(a, b)
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
