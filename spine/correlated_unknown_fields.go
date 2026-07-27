package spine

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/Project-Helianthus/helianthus-spine-go/api"
	"github.com/Project-Helianthus/helianthus-spine-go/model"
)

const (
	maxCorrelatedUnknownFields     = 256
	maxCorrelatedUnknownDepth      = 3
	maxCorrelatedUnknownPathBytes  = 512
	maxCorrelatedUnknownValueBytes = 16 * 1024
	maxCorrelatedUnknownTotalBytes = 256 * 1024
	maxCorrelatedJSONTreeDepth     = 64
	maxCorrelatedHeaderBytes       = 16 * 1024
)

var errCorrelatedDuplicateJSONMember = errors.New(
	"correlated response contains a duplicate JSON object member",
)

var errCorrelatedDuplicateIdentityMember = errors.New(
	"correlated response contains a duplicate correlation identity member",
)

type correlatedUnknownCollector struct {
	fields     []api.CorrelatedUnknownField
	totalBytes int
}

type correlatedJSONScanner struct {
	decoder   *json.Decoder
	collector *correlatedUnknownCollector
}

type correlatedJSONValueBudget struct {
	used          int
	totalBase     int
	maxValueBytes int
	maxTotalBytes int
}

func extractCorrelatedUnknownFields(message []byte) ([]api.CorrelatedUnknownField, error) {
	collector := correlatedUnknownCollector{
		fields: make([]api.CorrelatedUnknownField, 0),
	}
	scanner := newCorrelatedJSONScanner(message, &collector)
	if err := scanner.scanSchemaValue(
		reflect.TypeOf(model.Datagram{}),
		"",
		1,
	); err != nil {
		return nil, err
	}
	if err := scanner.requireEOF(); err != nil {
		return nil, err
	}

	sort.Slice(collector.fields, func(i, j int) bool {
		return collector.fields[i].Path < collector.fields[j].Path
	})
	return collector.fields, nil
}

func newCorrelatedJSONScanner(
	message []byte,
	collector *correlatedUnknownCollector,
) *correlatedJSONScanner {
	decoder := json.NewDecoder(bytes.NewReader(message))
	decoder.UseNumber()
	return &correlatedJSONScanner{
		decoder:   decoder,
		collector: collector,
	}
}

func (s *correlatedJSONScanner) requireEOF() error {
	if _, err := s.decoder.Token(); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return errors.New("decode original correlated response JSON")
	}
	return errors.New("correlated response contains trailing JSON data")
}

func (s *correlatedJSONScanner) scanSchemaValue(
	schema reflect.Type,
	path string,
	depth int,
) error {
	if depth > maxCorrelatedJSONTreeDepth {
		return errors.New("correlated response JSON tree exceeds the depth limit")
	}

	token, err := s.decoder.Token()
	if err != nil {
		return errors.New("decode original correlated response JSON")
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}

	schema = dereferenceCorrelatedJSONSchema(schema)
	switch delimiter {
	case '{':
		return s.scanSchemaObject(schema, path, depth)
	case '[':
		return s.scanSchemaArray(schema, path, depth)
	default:
		return errors.New("decode original correlated response JSON")
	}
}

func (s *correlatedJSONScanner) scanSchemaObject(
	schema reflect.Type,
	path string,
	depth int,
) error {
	var fields map[string]reflect.Type
	var mapElement reflect.Type
	if schema != nil {
		switch schema.Kind() {
		case reflect.Struct:
			fields = correlatedJSONStructFields(schema)
		case reflect.Map:
			if schema.Key().Kind() == reflect.String {
				mapElement = schema.Elem()
			}
		}
	}

	seen := make(map[string]struct{})
	for s.decoder.More() {
		token, err := s.decoder.Token()
		if err != nil {
			return errors.New("decode original correlated response JSON")
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("decode original correlated response JSON")
		}
		if _, duplicate := seen[key]; duplicate {
			return errCorrelatedDuplicateJSONMember
		}
		seen[key] = struct{}{}

		memberPath := path + "/" + escapeJSONPointerToken(key)
		if fieldType, known := fields[key]; known {
			if err := s.scanSchemaValue(fieldType, memberPath, depth+1); err != nil {
				return err
			}
			continue
		}
		if mapElement != nil {
			if err := s.scanSchemaValue(mapElement, memberPath, depth+1); err != nil {
				return err
			}
			continue
		}
		if fields != nil && s.collector != nil {
			if err := s.collector.addFromScanner(memberPath, s, depth+1); err != nil {
				return err
			}
			continue
		}
		if err := s.scanSchemaValue(nil, memberPath, depth+1); err != nil {
			return err
		}
	}
	return s.requireDelimiter('}')
}

func (s *correlatedJSONScanner) scanSchemaArray(
	schema reflect.Type,
	path string,
	depth int,
) error {
	var elementType reflect.Type
	if schema != nil {
		switch schema.Kind() {
		case reflect.Array, reflect.Slice:
			elementType = schema.Elem()
		}
	}

	index := 0
	for s.decoder.More() {
		memberPath := path + "/" + strconv.Itoa(index)
		if err := s.scanSchemaValue(elementType, memberPath, depth+1); err != nil {
			return err
		}
		index++
	}
	return s.requireDelimiter(']')
}

func (s *correlatedJSONScanner) requireDelimiter(want json.Delim) error {
	token, err := s.decoder.Token()
	if err != nil {
		return errors.New("decode original correlated response JSON")
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != want {
		return errors.New("decode original correlated response JSON")
	}
	return nil
}

func (c *correlatedUnknownCollector) addFromScanner(
	path string,
	scanner *correlatedJSONScanner,
	jsonDepth int,
) error {
	if len(path) > maxCorrelatedUnknownPathBytes {
		return errors.New("correlated unknown field path exceeds the byte limit")
	}
	if len(c.fields) == maxCorrelatedUnknownFields {
		return errors.New("correlated unknown field count exceeds the limit")
	}

	budget := correlatedJSONValueBudget{
		totalBase:     c.totalBytes,
		maxValueBytes: maxCorrelatedUnknownValueBytes,
		maxTotalBytes: maxCorrelatedUnknownTotalBytes,
	}
	value, err := scanner.parseBoundedValue(
		1,
		maxCorrelatedUnknownDepth,
		jsonDepth,
		&budget,
	)
	if err != nil {
		return err
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return errors.New("encode correlated unknown field value")
	}
	if len(encoded) != budget.used {
		return errors.New("correlated unknown field value size mismatch")
	}

	c.totalBytes += len(encoded)
	c.fields = append(c.fields, api.CorrelatedUnknownField{
		Path:  path,
		Value: api.CorrelatedUnknownValue(append(json.RawMessage(nil), encoded...)),
	})
	return nil
}

func (s *correlatedJSONScanner) parseBoundedValue(
	valueDepth int,
	maxValueDepth int,
	jsonDepth int,
	budget *correlatedJSONValueBudget,
) (any, error) {
	if valueDepth > maxValueDepth {
		return nil, errors.New("correlated unknown field value exceeds the depth limit")
	}
	if jsonDepth > maxCorrelatedJSONTreeDepth {
		return nil, errors.New("correlated response JSON tree exceeds the depth limit")
	}

	token, err := s.decoder.Token()
	if err != nil {
		return nil, errors.New("decode original correlated response JSON")
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		encoded, err := json.Marshal(token)
		if err != nil {
			return nil, errors.New("encode correlated unknown field value")
		}
		if err := budget.add(len(encoded)); err != nil {
			return nil, err
		}
		return token, nil
	}

	switch delimiter {
	case '{':
		return s.parseBoundedObject(
			valueDepth,
			maxValueDepth,
			jsonDepth,
			budget,
		)
	case '[':
		return s.parseBoundedArray(
			valueDepth,
			maxValueDepth,
			jsonDepth,
			budget,
		)
	default:
		return nil, errors.New("decode original correlated response JSON")
	}
}

func (s *correlatedJSONScanner) parseBoundedObject(
	valueDepth int,
	maxValueDepth int,
	jsonDepth int,
	budget *correlatedJSONValueBudget,
) (map[string]any, error) {
	if err := budget.add(2); err != nil {
		return nil, err
	}

	result := make(map[string]any)
	first := true
	for s.decoder.More() {
		token, err := s.decoder.Token()
		if err != nil {
			return nil, errors.New("decode original correlated response JSON")
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("decode original correlated response JSON")
		}
		if _, duplicate := result[key]; duplicate {
			return nil, errCorrelatedDuplicateJSONMember
		}

		encodedKey, err := json.Marshal(key)
		if err != nil {
			return nil, errors.New("encode correlated unknown field value")
		}
		overhead := len(encodedKey) + 1
		if !first {
			overhead++
		}
		if err := budget.add(overhead); err != nil {
			return nil, err
		}
		first = false

		value, err := s.parseBoundedValue(
			valueDepth+1,
			maxValueDepth,
			jsonDepth+1,
			budget,
		)
		if err != nil {
			return nil, err
		}
		result[key] = value
	}
	if err := s.requireDelimiter('}'); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *correlatedJSONScanner) parseBoundedArray(
	valueDepth int,
	maxValueDepth int,
	jsonDepth int,
	budget *correlatedJSONValueBudget,
) ([]any, error) {
	if err := budget.add(2); err != nil {
		return nil, err
	}

	result := make([]any, 0)
	for s.decoder.More() {
		if len(result) > 0 {
			if err := budget.add(1); err != nil {
				return nil, err
			}
		}
		value, err := s.parseBoundedValue(
			valueDepth+1,
			maxValueDepth,
			jsonDepth+1,
			budget,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	if err := s.requireDelimiter(']'); err != nil {
		return nil, err
	}
	return result, nil
}

func (b *correlatedJSONValueBudget) add(size int) error {
	if b.used > b.maxValueBytes-size {
		return errors.New("correlated unknown field value exceeds the byte limit")
	}
	if b.totalBase+b.used > b.maxTotalBytes-size {
		return errors.New("correlated unknown field values exceed the total byte limit")
	}
	b.used += size
	return nil
}

func dereferenceCorrelatedJSONSchema(schema reflect.Type) reflect.Type {
	for schema != nil && schema.Kind() == reflect.Pointer {
		schema = schema.Elem()
	}
	return schema
}

func correlatedJSONStructFields(schema reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type)
	for index := 0; index < schema.NumField(); index++ {
		field := schema.Field(index)
		if field.PkgPath != "" {
			continue
		}

		tag := field.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if field.Anonymous && name == "" {
			embedded := dereferenceCorrelatedJSONSchema(field.Type)
			if embedded.Kind() == reflect.Struct {
				for embeddedName, embeddedType := range correlatedJSONStructFields(embedded) {
					if _, exists := fields[embeddedName]; !exists {
						fields[embeddedName] = embeddedType
					}
				}
				continue
			}
		}
		if name == "" {
			name = field.Name
		}
		fields[name] = field.Type
	}
	return fields
}

func extractMalformedCorrelatedHeader(message []byte) (model.HeaderType, bool) {
	scanner := newCorrelatedJSONScanner(message, nil)
	header, found, err := scanner.scanMalformedEnvelope()
	if errors.Is(err, errCorrelatedDuplicateIdentityMember) {
		return model.HeaderType{}, false
	}
	return header, found
}

func (s *correlatedJSONScanner) scanMalformedEnvelope() (
	model.HeaderType,
	bool,
	error,
) {
	token, err := s.decoder.Token()
	if err != nil || token != json.Delim('{') {
		return model.HeaderType{}, false, err
	}

	seen := make(map[string]struct{})
	var candidate model.HeaderType
	found := false
	for s.decoder.More() {
		token, err := s.decoder.Token()
		if err != nil {
			return candidate, found, err
		}
		key, ok := token.(string)
		if !ok {
			return candidate, found, errors.New("invalid SPINE message envelope")
		}
		if _, duplicate := seen[key]; duplicate {
			return model.HeaderType{}, false, errCorrelatedDuplicateIdentityMember
		}
		seen[key] = struct{}{}

		if key == "datagram" {
			header, headerFound, err := s.scanMalformedDatagram(2)
			if headerFound {
				candidate = header
				found = true
			}
			if err != nil {
				return candidate, found, err
			}
			continue
		}
		if err := s.scanSchemaValue(nil, "", 2); err != nil {
			return candidate, found, err
		}
	}
	if err := s.requireDelimiter('}'); err != nil {
		return candidate, found, err
	}
	if err := s.requireEOF(); err != nil {
		return candidate, found, err
	}
	return candidate, found, nil
}

func (s *correlatedJSONScanner) scanMalformedDatagram(
	depth int,
) (model.HeaderType, bool, error) {
	token, err := s.decoder.Token()
	if err != nil || token != json.Delim('{') {
		return model.HeaderType{}, false, err
	}

	seen := make(map[string]struct{})
	var candidate model.HeaderType
	found := false
	for s.decoder.More() {
		token, err := s.decoder.Token()
		if err != nil {
			return candidate, found, err
		}
		key, ok := token.(string)
		if !ok {
			return candidate, found, errors.New("invalid SPINE datagram")
		}
		if _, duplicate := seen[key]; duplicate {
			return model.HeaderType{}, false, errCorrelatedDuplicateIdentityMember
		}
		seen[key] = struct{}{}

		if key == "header" {
			budget := correlatedJSONValueBudget{
				maxValueBytes: maxCorrelatedHeaderBytes,
				maxTotalBytes: maxCorrelatedHeaderBytes,
			}
			value, err := s.parseBoundedValue(
				1,
				maxCorrelatedJSONTreeDepth,
				depth+1,
				&budget,
			)
			if err != nil {
				if errors.Is(err, errCorrelatedDuplicateJSONMember) {
					return model.HeaderType{}, false, errCorrelatedDuplicateIdentityMember
				}
				return candidate, found, err
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				return candidate, found, err
			}
			var header model.HeaderType
			if err := json.Unmarshal(encoded, &header); err != nil {
				return candidate, found, err
			}
			candidate = header
			found = true
			continue
		}
		if err := s.scanSchemaValue(nil, "", depth+1); err != nil {
			return candidate, found, err
		}
	}
	if err := s.requireDelimiter('}'); err != nil {
		return candidate, found, err
	}
	return candidate, found, nil
}

func escapeJSONPointerToken(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}
