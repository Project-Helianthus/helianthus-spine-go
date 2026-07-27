package spine

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
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
)

type correlatedUnknownCollector struct {
	fields     []api.CorrelatedUnknownField
	totalBytes int
}

func extractCorrelatedUnknownFields(
	message []byte,
	datagram model.DatagramType,
) ([]api.CorrelatedUnknownField, error) {
	original, err := decodeCorrelatedJSONTree(message)
	if err != nil {
		return nil, err
	}

	typedMessage, err := json.Marshal(model.Datagram{Datagram: datagram})
	if err != nil {
		return nil, errors.New("encode typed correlated response tree")
	}
	typed, err := decodeCorrelatedJSONTree(typedMessage)
	if err != nil {
		return nil, errors.New("decode typed correlated response tree")
	}

	collector := correlatedUnknownCollector{
		fields: make([]api.CorrelatedUnknownField, 0),
	}
	if err := collector.diff(original, typed, "", 0); err != nil {
		return nil, err
	}
	sort.Slice(collector.fields, func(i, j int) bool {
		return collector.fields[i].Path < collector.fields[j].Path
	})
	return collector.fields, nil
}

func decodeCorrelatedJSONTree(message []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(message))
	decoder.UseNumber()

	var tree any
	if err := decoder.Decode(&tree); err != nil {
		return nil, errors.New("decode original correlated response JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("correlated response contains trailing JSON data")
	}
	return tree, nil
}

func (c *correlatedUnknownCollector) diff(
	original any,
	typed any,
	path string,
	depth int,
) error {
	if depth > maxCorrelatedJSONTreeDepth {
		return errors.New("correlated response JSON tree exceeds the depth limit")
	}

	switch originalValue := original.(type) {
	case map[string]any:
		typedValue, ok := typed.(map[string]any)
		if !ok {
			return nil
		}

		keys := make([]string, 0, len(originalValue))
		for key := range originalValue {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			originalMember := originalValue[key]
			memberPath := path + "/" + escapeJSONPointerToken(key)
			typedMember, represented := typedValue[key]
			if !represented {
				if err := c.add(memberPath, originalMember); err != nil {
					return err
				}
				continue
			}
			if err := c.diff(originalMember, typedMember, memberPath, depth+1); err != nil {
				return err
			}
		}

	case []any:
		typedValue, ok := typed.([]any)
		if !ok {
			return nil
		}
		length := len(originalValue)
		if len(typedValue) < length {
			length = len(typedValue)
		}
		for index := 0; index < length; index++ {
			memberPath := path + "/" + strconv.Itoa(index)
			if err := c.diff(
				originalValue[index],
				typedValue[index],
				memberPath,
				depth+1,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *correlatedUnknownCollector) add(path string, value any) error {
	if len(path) > maxCorrelatedUnknownPathBytes {
		return errors.New("correlated unknown field path exceeds the byte limit")
	}
	if len(c.fields) == maxCorrelatedUnknownFields {
		return errors.New("correlated unknown field count exceeds the limit")
	}
	if err := validateCorrelatedUnknownValueDepth(value, 1); err != nil {
		return err
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return errors.New("encode correlated unknown field value")
	}
	if len(encoded) > maxCorrelatedUnknownValueBytes {
		return errors.New("correlated unknown field value exceeds the byte limit")
	}
	if c.totalBytes > maxCorrelatedUnknownTotalBytes-len(encoded) {
		return errors.New("correlated unknown field values exceed the total byte limit")
	}

	c.totalBytes += len(encoded)
	c.fields = append(c.fields, api.CorrelatedUnknownField{
		Path:  path,
		Value: api.CorrelatedUnknownValue(append(json.RawMessage(nil), encoded...)),
	})
	return nil
}

func validateCorrelatedUnknownValueDepth(value any, depth int) error {
	if depth > maxCorrelatedUnknownDepth {
		return errors.New("correlated unknown field value exceeds the depth limit")
	}

	switch typed := value.(type) {
	case map[string]any:
		for _, member := range typed {
			if err := validateCorrelatedUnknownValueDepth(member, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, member := range typed {
			if err := validateCorrelatedUnknownValueDepth(member, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func escapeJSONPointerToken(token string) string {
	return strings.ReplaceAll(strings.ReplaceAll(token, "~", "~0"), "/", "~1")
}
