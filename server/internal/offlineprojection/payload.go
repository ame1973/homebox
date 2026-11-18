// Package offlineprojection validates version-one offline payload structure.
// It neither proves source authority nor enables bootstrap or delta reads.
package offlineprojection

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"homebox/internal/strictjson"
)

//go:embed offline-projection.schema.json
var contract []byte

var (
	// ErrInvalidPayload never includes untrusted projection data.
	ErrInvalidPayload = errors.New("invalid offline projection payload")
	// ErrSchemaUnavailable means the embedded contract cannot be safely used.
	ErrSchemaUnavailable = errors.New("offline projection schema unavailable")
	compiledRules        = sync.OnceValues(compileContract)
	dateTimePattern      = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}[Tt][0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?([Zz]|[+-][0-9]{2}:[0-9]{2})$`)
)

var projectionKinds = []string{
	"location", "item", "unit", "stock", "barcode", "media", "media_link", "category", "tag", "item_tag", "search_doc",
	"purchase", "purchase_line", "asset", "usage", "cost", "disposal", "alias", "attribute", "location_event", "inventory_event", "ai_review",
}

type contractDocument struct {
	Schema      string                     `json:"$schema"`
	ID          string                     `json:"$id"`
	Title       string                     `json:"title"`
	OneOf       []json.RawMessage          `json:"oneOf"`
	Definitions map[string]json.RawMessage `json:"$defs"`
}

type contractObject struct {
	Type                 string                     `json:"type"`
	AdditionalProperties *bool                      `json:"additionalProperties"`
	Required             []string                   `json:"required"`
	Properties           map[string]json.RawMessage `json:"properties"`
	AllOf                []json.RawMessage          `json:"allOf"`
}

type contractField struct {
	Type      json.RawMessage   `json:"type"`
	Enum      []json.RawMessage `json:"enum"`
	MinLength *int              `json:"minLength"`
	MaxLength *int              `json:"maxLength"`
	Minimum   *int64            `json:"minimum"`
	Maximum   *int64            `json:"maximum"`
	Pattern   *string           `json:"pattern"`
	Format    *string           `json:"format"`
	AnyOf     []json.RawMessage `json:"anyOf"`
}

type contractFragment struct {
	Properties map[string]json.RawMessage `json:"properties"`
}

type contractConditional struct {
	If   json.RawMessage `json:"if"`
	Then json.RawMessage `json:"then"`
}

type fieldRule struct {
	types                map[string]bool
	enum                 []string
	minLength, maxLength *int
	minimum, maximum     *int64
	pattern              *regexp.Regexp
	dateTime             bool
	alternatives         []fieldRule
}

type conditionalRule struct {
	ifFields   map[string]fieldRule
	thenFields map[string]fieldRule
}

type objectRule struct {
	fields     map[string]fieldRule
	required   map[string]bool
	conditions []conditionalRule
}

// decodeContract rejects unknown schema keywords rather than silently treating
// a future contract extension as an already-supported validation rule.
func decodeContract(raw []byte, target any) error {
	var allowed []string
	switch target.(type) {
	case *contractDocument:
		allowed = []string{"$schema", "$id", "title", "oneOf", "$defs"}
	case *contractObject:
		allowed = []string{"type", "additionalProperties", "required", "properties", "allOf"}
	case *contractField:
		allowed = []string{"type", "enum", "minLength", "maxLength", "minimum", "maximum", "pattern", "format", "anyOf"}
	case *contractFragment:
		allowed = []string{"properties"}
	case *contractConditional:
		allowed = []string{"if", "then"}
	default:
		return errors.New("unsupported schema target")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return errors.New("invalid schema object")
	}
	for key, value := range fields {
		known := false
		for _, name := range allowed {
			if key == name {
				known = true
				break
			}
		}
		if !known || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("unsupported schema keyword")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing schema data")
	}
	return nil
}

// Schema compilation is bounded and rejects duplicate names at every depth.
func scanSchemaValue(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("schema nesting limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			name, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := name.(string)
			if !ok || seen[key] {
				return errors.New("duplicate schema field")
			}
			seen[key] = true
			if err := scanSchemaValue(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("invalid schema object")
		}
	case '[':
		for decoder.More() {
			if err := scanSchemaValue(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("invalid schema array")
		}
	default:
		return errors.New("invalid schema delimiter")
	}
	return nil
}

func compileContract() (map[string]objectRule, error) {
	return compileContractBytes(contract)
}

func compileContractBytes(data []byte) (map[string]objectRule, error) {
	if len(data) > 4<<20 || !strictjson.ValidUnicodeJSON(data) {
		return nil, ErrSchemaUnavailable
	}
	scanner := json.NewDecoder(bytes.NewReader(data))
	scanner.UseNumber()
	if err := scanSchemaValue(scanner, 0); err != nil {
		return nil, ErrSchemaUnavailable
	}
	var document contractDocument
	if err := decodeContract(data, &document); err != nil ||
		document.Schema != "https://json-schema.org/draft/2020-12/schema" ||
		document.ID != "https://homebox.invalid/contracts/offline_projection.v0.9.schema.json" ||
		len(document.OneOf) != len(projectionKinds)+1 || len(document.Definitions) != len(projectionKinds) {
		return nil, ErrSchemaUnavailable
	}
	rules := make(map[string]objectRule, len(projectionKinds))
	for _, kind := range projectionKinds {
		raw, found := document.Definitions[kind]
		if !found {
			return nil, ErrSchemaUnavailable
		}
		rule, err := compileObject(raw)
		if err != nil {
			return nil, ErrSchemaUnavailable
		}
		rules[kind] = rule
	}
	return rules, nil
}

func compileObject(raw []byte) (objectRule, error) {
	var input contractObject
	if err := decodeContract(raw, &input); err != nil || input.Type != "object" || input.AdditionalProperties == nil || *input.AdditionalProperties || len(input.Properties) == 0 || len(input.Required) != len(input.Properties) {
		return objectRule{}, errors.New("unsupported object schema")
	}
	result := objectRule{fields: make(map[string]fieldRule, len(input.Properties)), required: make(map[string]bool, len(input.Required))}
	for name, value := range input.Properties {
		if name == "" {
			return objectRule{}, errors.New("empty property name")
		}
		rule, err := compileField(value, 0)
		if err != nil {
			return objectRule{}, err
		}
		result.fields[name] = rule
	}
	for _, name := range input.Required {
		if _, found := result.fields[name]; !found || result.required[name] {
			return objectRule{}, errors.New("invalid required property")
		}
		result.required[name] = true
	}
	for _, raw := range input.AllOf {
		var condition contractConditional
		var ifFragment, thenFragment contractFragment
		if err := decodeContract(raw, &condition); err != nil || len(condition.If) == 0 || len(condition.Then) == 0 {
			return objectRule{}, errors.New("unsupported conditional schema")
		}
		if err := decodeContract(condition.If, &ifFragment); err != nil || len(ifFragment.Properties) == 0 {
			return objectRule{}, errors.New("unsupported conditional schema")
		}
		if err := decodeContract(condition.Then, &thenFragment); err != nil || len(thenFragment.Properties) == 0 {
			return objectRule{}, errors.New("unsupported conditional schema")
		}
		ifFields, err := compileFragment(ifFragment.Properties, result.fields)
		if err != nil {
			return objectRule{}, err
		}
		thenFields, err := compileFragment(thenFragment.Properties, result.fields)
		if err != nil {
			return objectRule{}, err
		}
		result.conditions = append(result.conditions, conditionalRule{ifFields: ifFields, thenFields: thenFields})
	}
	return result, nil
}

func compileFragment(fields map[string]json.RawMessage, known map[string]fieldRule) (map[string]fieldRule, error) {
	result := make(map[string]fieldRule, len(fields))
	for name, raw := range fields {
		if _, found := known[name]; !found {
			return nil, errors.New("conditional references unknown property")
		}
		rule, err := compileField(raw, 0)
		if err != nil {
			return nil, err
		}
		result[name] = rule
	}
	return result, nil
}

func compileField(raw []byte, depth int) (fieldRule, error) {
	if depth > 4 {
		return fieldRule{}, errors.New("field nesting limit")
	}
	var input contractField
	if err := decodeContract(raw, &input); err != nil {
		return fieldRule{}, err
	}
	result := fieldRule{minLength: input.MinLength, maxLength: input.MaxLength, minimum: input.Minimum, maximum: input.Maximum}
	if input.Enum != nil {
		result.enum = make([]string, 0, len(input.Enum))
		for _, raw := range input.Enum {
			var text string
			if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &text) != nil {
				return fieldRule{}, errors.New("unsupported enum member")
			}
			result.enum = append(result.enum, text)
		}
	}
	if len(input.Type) > 0 {
		var types []string
		var single string
		if err := json.Unmarshal(input.Type, &single); err == nil {
			types = []string{single}
		} else if err := json.Unmarshal(input.Type, &types); err != nil {
			return fieldRule{}, err
		}
		if len(types) == 0 {
			return fieldRule{}, errors.New("empty field type")
		}
		result.types = make(map[string]bool, len(types))
		for _, kind := range types {
			if kind != "string" && kind != "integer" && kind != "boolean" && kind != "null" || result.types[kind] {
				return fieldRule{}, errors.New("unsupported field type")
			}
			result.types[kind] = true
		}
	}
	if input.Enum != nil && len(input.Enum) == 0 || input.AnyOf != nil && (len(input.AnyOf) == 0 || len(input.AnyOf) > 8) || len(result.types) == 0 && input.Enum == nil && input.AnyOf == nil {
		return fieldRule{}, errors.New("unsupported unconstrained field")
	}
	if input.MinLength != nil && *input.MinLength < 0 || input.MaxLength != nil && *input.MaxLength < 0 ||
		input.MinLength != nil && input.MaxLength != nil && *input.MinLength > *input.MaxLength ||
		input.Minimum != nil && input.Maximum != nil && *input.Minimum > *input.Maximum {
		return fieldRule{}, errors.New("invalid field limits")
	}
	if input.Pattern != nil {
		if *input.Pattern != `^[a-f0-9]{64}$` && *input.Pattern != `^[A-Z]{3}$` {
			return fieldRule{}, errors.New("unsupported pattern")
		}
		compiled, err := regexp.Compile(*input.Pattern)
		if err != nil {
			return fieldRule{}, err
		}
		result.pattern = compiled
	}
	if input.Format != nil {
		if *input.Format != "date-time" {
			return fieldRule{}, errors.New("unsupported format")
		}
		result.dateTime = true
	}
	for _, alternative := range input.AnyOf {
		rule, err := compileField(alternative, depth+1)
		if err != nil {
			return fieldRule{}, err
		}
		result.alternatives = append(result.alternatives, rule)
	}
	return result, nil
}

func (rule fieldRule) valid(value any) bool {
	kind := ""
	switch value.(type) {
	case nil:
		kind = "null"
	case string:
		kind = "string"
	case bool:
		kind = "boolean"
	case int64:
		kind = "integer"
	default:
		return false
	}
	if len(rule.types) != 0 && !rule.types[kind] {
		return false
	}
	if rule.enum != nil {
		text, ok := value.(string)
		if !ok {
			return false
		}
		found := false
		for _, allowed := range rule.enum {
			if text == allowed {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if text, ok := value.(string); ok {
		length := utf8.RuneCountInString(text)
		if rule.minLength != nil && length < *rule.minLength || rule.maxLength != nil && length > *rule.maxLength ||
			rule.pattern != nil && !rule.pattern.MatchString(text) || rule.dateTime && !validDateTime(text) {
			return false
		}
	}
	if number, ok := value.(int64); ok {
		if rule.minimum != nil && number < *rule.minimum || rule.maximum != nil && number > *rule.maximum {
			return false
		}
	}
	if rule.alternatives != nil {
		found := false
		for _, alternative := range rule.alternatives {
			if alternative.valid(value) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func validDateTime(value string) bool {
	if !dateTimePattern.MatchString(value) {
		return false
	}
	canonical := strings.ToUpper(value)
	if canonical[len(canonical)-1] != 'Z' {
		zone := canonical[len(canonical)-6:]
		hours := int(zone[1]-'0')*10 + int(zone[2]-'0')
		minutes := int(zone[4]-'0')*10 + int(zone[5]-'0')
		if hours > 23 || minutes > 59 {
			return false
		}
	}
	instant, err := time.Parse(time.RFC3339Nano, canonical)
	return err == nil && instant.Year() >= 1 && instant.Year() <= 9999
}

func matchesFragment(fields map[string]fieldRule, values map[string]any) bool {
	for name, rule := range fields {
		if value, exists := values[name]; exists && !rule.valid(value) {
			return false
		}
	}
	return true
}

func (rule objectRule) validate(body []byte) error {
	if !strictjson.ValidUnicodeJSON(body) {
		return ErrInvalidPayload
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return ErrInvalidPayload
	}
	values := make(map[string]any, len(rule.fields))
	for decoder.More() {
		name, err := decoder.Token()
		if err != nil {
			return ErrInvalidPayload
		}
		key, ok := name.(string)
		field, known := rule.fields[key]
		if !ok || !known {
			return ErrInvalidPayload
		}
		if _, duplicate := values[key]; duplicate {
			return ErrInvalidPayload
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil || len(raw) == 0 {
			return ErrInvalidPayload
		}
		var value any
		switch raw[0] {
		case '"':
			var text string
			if err := json.Unmarshal(raw, &text); err != nil {
				return ErrInvalidPayload
			}
			value = text
		case 't', 'f':
			var flag bool
			if err := json.Unmarshal(raw, &flag); err != nil {
				return ErrInvalidPayload
			}
			value = flag
		case 'n':
			if string(raw) != "null" {
				return ErrInvalidPayload
			}
		default:
			number, ok := strictjson.ExactSafeInteger(raw)
			if !ok {
				return ErrInvalidPayload
			}
			value = number
		}
		if !field.valid(value) {
			return ErrInvalidPayload
		}
		values[key] = value
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return ErrInvalidPayload
	}
	for key := range rule.required {
		if _, exists := values[key]; !exists {
			return ErrInvalidPayload
		}
	}
	for _, condition := range rule.conditions {
		if matchesFragment(condition.ifFields, values) && !matchesFragment(condition.thenFields, values) {
			return ErrInvalidPayload
		}
	}
	return nil
}

// ValidatePayload validates the complete flat payload for one known kind. The
// caller must bound its input and separately verify identity and source approval.
func ValidatePayload(kind string, body []byte) error {
	rules, err := compiledRules()
	if err != nil {
		return ErrSchemaUnavailable
	}
	rule, found := rules[kind]
	if !found {
		return ErrInvalidPayload
	}
	if err := rule.validate(body); err != nil {
		return ErrInvalidPayload
	}
	return nil
}
