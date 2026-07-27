package spine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Project-Helianthus/helianthus-spine-go/api"
	"github.com/Project-Helianthus/helianthus-spine-go/model"
	"github.com/Project-Helianthus/helianthus-spine-go/util"
)

const (
	issue11MaxUnknownFields     = 256
	issue11MaxUnknownDepth      = 3
	issue11MaxUnknownPathBytes  = 512
	issue11MaxUnknownValueBytes = 16 * 1024
	issue11MaxUnknownTotalBytes = 256 * 1024
)

type issue11UnknownObservation struct {
	path  string
	value []byte
}

func issue11ResponseWithMutation(
	t *testing.T,
	request model.DatagramType,
	mutate func(map[string]any),
) []byte {
	t.Helper()

	return issue11MessageWithMutation(
		t,
		correlatedResponse(
			request,
			model.CmdClassifierTypeReply,
			[]model.CmdType{{
				DeviceClassificationManufacturerData: &model.DeviceClassificationManufacturerDataType{},
			}},
		),
		mutate,
	)
}

func issue11MessageWithMutation(
	t *testing.T,
	message []byte,
	mutate func(map[string]any),
) []byte {
	t.Helper()

	var tree map[string]any
	decoder := json.NewDecoder(bytes.NewReader(message))
	decoder.UseNumber()
	if err := decoder.Decode(&tree); err != nil {
		t.Fatalf("decode correlated response fixture: %v", err)
	}
	mutate(tree)

	result, err := json.Marshal(tree)
	if err != nil {
		t.Fatalf("encode correlated response fixture: %v", err)
	}
	return result
}

func issue11ReplaceOnce(t *testing.T, message, old, replacement []byte) []byte {
	t.Helper()
	if count := bytes.Count(message, old); count != 1 {
		t.Fatalf("fixture marker %q count = %d, want 1", old, count)
	}
	return bytes.Replace(message, old, replacement, 1)
}

func issue11Object(t *testing.T, value any, path string) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s type = %T, want object", path, value)
	}
	return object
}

func issue11Array(t *testing.T, value any, path string) []any {
	t.Helper()
	array, ok := value.([]any)
	if !ok {
		t.Fatalf("%s type = %T, want array", path, value)
	}
	return array
}

func issue11DatagramTree(t *testing.T, tree map[string]any) map[string]any {
	t.Helper()
	return issue11Object(t, tree["datagram"], "/datagram")
}

func issue11CommandTree(t *testing.T, tree map[string]any) map[string]any {
	t.Helper()
	datagram := issue11DatagramTree(t, tree)
	payload := issue11Object(t, datagram["payload"], "/datagram/payload")
	cmds := issue11Array(t, payload["cmd"], "/datagram/payload/cmd")
	if len(cmds) != 1 {
		t.Fatalf("fixture command count = %d, want 1", len(cmds))
	}
	return issue11Object(t, cmds[0], "/datagram/payload/cmd/0")
}

func issue11UnknownFields(
	t *testing.T,
	response api.CorrelatedResponse,
) []issue11UnknownObservation {
	t.Helper()

	value := reflect.ValueOf(response)
	field := value.FieldByName("UnknownFields")
	if !field.IsValid() {
		t.Fatal("CorrelatedResponse.UnknownFields is missing; nested unknown response members were discarded")
	}
	if field.Kind() != reflect.Slice {
		t.Fatalf("CorrelatedResponse.UnknownFields kind = %s, want slice", field.Kind())
	}

	result := make([]issue11UnknownObservation, field.Len())
	for index := 0; index < field.Len(); index++ {
		entry := field.Index(index)
		path := entry.FieldByName("Path")
		raw := entry.FieldByName("Value")
		if !path.IsValid() || path.Kind() != reflect.String {
			t.Fatalf("unknown field %d has no string Path", index)
		}
		if !raw.IsValid() || raw.Kind() != reflect.Slice ||
			raw.Type().Elem().Kind() != reflect.Uint8 {
			t.Fatalf("unknown field %d has invalid Value type %s", index, raw.Type())
		}
		result[index] = issue11UnknownObservation{
			path:  path.String(),
			value: append([]byte(nil), raw.Bytes()...),
		}
	}
	return result
}

func issue11StartAndReceive(
	t *testing.T,
	fixture *correlatedFixture,
	message func(model.DatagramType) []byte,
) correlatedResult {
	t.Helper()

	result := startCorrelatedRoundTrip(
		context.Background(),
		fixture.roundTripper,
		fixture.request,
	)
	request := fixture.sender.writeHandler.(*correlatedWriteRecorder).next(t)
	if _, err := fixture.remote.HandleSpineMesssage(message(request)); err != nil {
		t.Logf("HandleSpineMesssage() error = %v", err)
	}
	return receiveCorrelatedResult(t, result)
}

func issue11AssertProtocolFailure(
	t *testing.T,
	mutate func(map[string]any),
) {
	t.Helper()

	writer := newCorrelatedWriteRecorder()
	fixture := newCorrelatedFixture(t, writer, "issue11-limit")
	result := startCorrelatedRoundTrip(
		context.Background(),
		fixture.roundTripper,
		fixture.request,
	)
	request := writer.next(t)
	message := issue11ResponseWithMutation(t, request, mutate)
	_, _ = fixture.remote.HandleSpineMesssage(message)

	got := receiveCorrelatedResult(t, result)
	var protocolErr *api.CorrelatedProtocolError
	if !errors.As(got.err, &protocolErr) {
		t.Fatalf("RoundTrip() error = %T %v, want CorrelatedProtocolError", got.err, got.err)
	}
	if got.response.CorrelationKey != 0 {
		t.Fatalf("failed response correlation key = %d, want zero response", got.response.CorrelationKey)
	}
	if fixture.roundTripper.Stats().InFlight != 0 {
		t.Fatalf("in-flight count after protocol failure = %d, want 0", fixture.roundTripper.Stats().InFlight)
	}
	if cached, _ := fixture.remoteFeature.DataCopy(
		model.FunctionTypeDeviceClassificationManufacturerData,
	).(*model.DeviceClassificationManufacturerDataType); cached != nil {
		t.Fatal("over-limit response mutated the legacy feature cache before admission failed")
	}
}

func issue11AssertRawProtocolFailure(
	t *testing.T,
	message func(model.DatagramType) []byte,
) {
	t.Helper()

	writer := newCorrelatedWriteRecorder()
	fixture := newCorrelatedFixture(t, writer, "issue11-raw-failure")
	got := issue11StartAndReceive(t, fixture, message)
	var protocolErr *api.CorrelatedProtocolError
	if !errors.As(got.err, &protocolErr) {
		t.Fatalf("RoundTrip() error = %T %v, want CorrelatedProtocolError", got.err, got.err)
	}
	if fixture.roundTripper.Stats().InFlight != 0 {
		t.Fatalf("in-flight count after protocol failure = %d, want 0", fixture.roundTripper.Stats().InFlight)
	}
}

func TestIssue11KnownOnlyResponseHasEmptyUnknownCarrier(t *testing.T) {
	writer := newCorrelatedWriteRecorder()
	fixture := newCorrelatedFixture(t, writer, "issue11-known")
	got := issue11StartAndReceive(t, fixture, func(request model.DatagramType) []byte {
		return correlatedResponse(
			request,
			model.CmdClassifierTypeReply,
			[]model.CmdType{fixture.reply},
		)
	})
	if got.err != nil {
		t.Fatalf("RoundTrip() error = %v", got.err)
	}
	if unknowns := issue11UnknownFields(t, got.response); len(unknowns) != 0 {
		t.Fatalf("known-only unknown fields = %+v, want empty", unknowns)
	}
}

func TestIssue11SchemaKnownNullAndEmptyCollectionsAreNotUnknown(t *testing.T) {
	writer := newCorrelatedWriteRecorder()
	fixture := newCorrelatedFixture(t, writer, "issue11-schema-known-empty")
	got := issue11StartAndReceive(t, fixture, func(request model.DatagramType) []byte {
		return issue11ResponseWithMutation(t, request, func(tree map[string]any) {
			header := issue11Object(
				t,
				issue11DatagramTree(t, tree)["header"],
				"/datagram/header",
			)
			header["addressOriginator"] = map[string]any{
				"device":  nil,
				"entity":  []any{},
				"feature": nil,
			}
			manufacturer := issue11Object(
				t,
				issue11CommandTree(t, tree)["deviceClassificationManufacturerData"],
				"/datagram/payload/cmd/0/deviceClassificationManufacturerData",
			)
			manufacturer["deviceName"] = nil
		})
	})
	if got.err != nil {
		t.Fatalf("RoundTrip() error = %v", got.err)
	}
	if unknowns := issue11UnknownFields(t, got.response); len(unknowns) != 0 {
		t.Fatalf("schema-known null/empty fields classified unknown: %+v", unknowns)
	}
}

func TestIssue11NestedUnknownResponseMembersArePreservedDeterministically(t *testing.T) {
	writer := newCorrelatedWriteRecorder()
	fixture := newCorrelatedFixture(t, writer, "issue11-nested")
	got := issue11StartAndReceive(t, fixture, func(request model.DatagramType) []byte {
		return issue11ResponseWithMutation(t, request, func(tree map[string]any) {
			tree["rootExtension"] = map[string]any{"enabled": true}
			datagram := issue11DatagramTree(t, tree)
			datagram["frameExtension"] = json.Number("17")
			header := issue11Object(t, datagram["header"], "/datagram/header")
			header["headerExtension"] = "header"
			cmd := issue11CommandTree(t, tree)
			cmd["zExtension"] = []any{"z", map[string]any{"nested": true}}
			manufacturer := issue11Object(
				t,
				cmd["deviceClassificationManufacturerData"],
				"/datagram/payload/cmd/0/deviceClassificationManufacturerData",
			)
			manufacturer["vendor/~flag"] = map[string]any{"b": 2, "a": 1}
		})
	})
	if got.err != nil {
		t.Fatalf("RoundTrip() error = %v", got.err)
	}

	unknowns := issue11UnknownFields(t, got.response)
	paths := make([]string, len(unknowns))
	for index := range unknowns {
		paths[index] = unknowns[index].path
	}
	wantPaths := []string{
		"/datagram/frameExtension",
		"/datagram/header/headerExtension",
		"/datagram/payload/cmd/0/deviceClassificationManufacturerData/vendor~1~0flag",
		"/datagram/payload/cmd/0/zExtension",
		"/rootExtension",
	}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("unknown paths = %#v, want %#v", paths, wantPaths)
	}
	if !sort.StringsAreSorted(paths) {
		t.Fatalf("unknown paths are not deterministic: %#v", paths)
	}
	if gotValue := string(unknowns[2].value); gotValue != `{"a":1,"b":2}` {
		t.Fatalf("nested unknown value = %s, want canonical object", gotValue)
	}
}

func TestIssue11UnknownNumberLexemeIsPreserved(t *testing.T) {
	writer := newCorrelatedWriteRecorder()
	fixture := newCorrelatedFixture(t, writer, "issue11-number-lexeme")
	got := issue11StartAndReceive(t, fixture, func(request model.DatagramType) []byte {
		return issue11ResponseWithMutation(t, request, func(tree map[string]any) {
			issue11CommandTree(t, tree)["numberExtension"] = json.Number("1.2300e+04")
		})
	})
	if got.err != nil {
		t.Fatalf("RoundTrip() error = %v", got.err)
	}
	unknowns := issue11UnknownFields(t, got.response)
	if len(unknowns) != 1 || string(unknowns[0].value) != "1.2300e+04" {
		t.Fatalf("number unknowns = %+v, want preserved 1.2300e+04 lexeme", unknowns)
	}
}

func TestIssue11DuplicateKeysAtKnownAndUnknownDepthFailClosed(t *testing.T) {
	t.Run("known header object", func(t *testing.T) {
		issue11AssertRawProtocolFailure(t, func(request model.DatagramType) []byte {
			message := correlatedResponse(
				request,
				model.CmdClassifierTypeReply,
				[]model.CmdType{{
					DeviceClassificationManufacturerData: &model.DeviceClassificationManufacturerDataType{},
				}},
			)
			marker := []byte(fmt.Sprintf(
				`"msgCounterReference":%d`,
				*request.Header.MsgCounter,
			))
			replacement := []byte(fmt.Sprintf(
				`"msgCounterReference":%d,"msgCounterReference":%d`,
				*request.Header.MsgCounter,
				*request.Header.MsgCounter,
			))
			return issue11ReplaceOnce(t, message, marker, replacement)
		})
	})

	t.Run("nested unknown object", func(t *testing.T) {
		issue11AssertRawProtocolFailure(t, func(request model.DatagramType) []byte {
			message := issue11ResponseWithMutation(t, request, func(tree map[string]any) {
				issue11CommandTree(t, tree)["duplicateExtension"] =
					map[string]any{"member": json.Number("1")}
			})
			return issue11ReplaceOnce(
				t,
				message,
				[]byte(`"duplicateExtension":{"member":1}`),
				[]byte(`"duplicateExtension":{"member":1,"member":2}`),
			)
		})
	})
}

func TestIssue11UnknownValuesAreDeepCopiedAndFormattingIsNonDisclosing(t *testing.T) {
	const secret = "issue11-format-secret"

	writer := newCorrelatedWriteRecorder()
	fixture := newCorrelatedFixture(t, writer, "issue11-copy")
	result := startCorrelatedRoundTrip(
		context.Background(),
		fixture.roundTripper,
		fixture.request,
	)
	request := writer.next(t)
	message := issue11ResponseWithMutation(t, request, func(tree map[string]any) {
		issue11CommandTree(t, tree)["copyExtension"] = map[string]any{"value": secret}
	})
	if _, err := fixture.remote.HandleSpineMesssage(message); err != nil {
		t.Fatalf("HandleSpineMesssage() error = %v", err)
	}
	got := receiveCorrelatedResult(t, result)
	if got.err != nil {
		t.Fatalf("RoundTrip() error = %v", got.err)
	}

	for index := range message {
		message[index] = 'x'
	}
	unknowns := issue11UnknownFields(t, got.response)
	if len(unknowns) != 1 || !bytes.Contains(unknowns[0].value, []byte(secret)) {
		t.Fatalf("unknown fields changed after input mutation: %+v", unknowns)
	}

	responseValue := reflect.ValueOf(got.response)
	entryValue := responseValue.FieldByName("UnknownFields").Index(0)
	entry := entryValue.Interface()
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		rendered := fmt.Sprintf(format, entry)
		if strings.Contains(rendered, secret) {
			t.Errorf("format %q disclosed unknown value", format)
		}
		rendered = fmt.Sprintf(format, entryValue.FieldByName("Value").Interface())
		if strings.Contains(rendered, secret) {
			t.Errorf("value format %q disclosed unknown value", format)
		}
	}
	if rendered := fmt.Sprintf("%+v", got.response); strings.Contains(rendered, secret) {
		t.Fatal("CorrelatedResponse formatting disclosed an unknown value")
	}
}

func TestIssue11UnknownFieldHardLimitsFailClosed(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		issue11AssertProtocolFailure(t, func(tree map[string]any) {
			datagram := issue11DatagramTree(t, tree)
			for index := 0; index <= issue11MaxUnknownFields; index++ {
				datagram[fmt.Sprintf("extension%03d", index)] = index
			}
		})
	})

	t.Run("depth", func(t *testing.T) {
		issue11AssertProtocolFailure(t, func(tree map[string]any) {
			var value any = "leaf"
			for index := 0; index <= issue11MaxUnknownDepth; index++ {
				value = map[string]any{"nested": value}
			}
			issue11CommandTree(t, tree)["deepExtension"] = value
		})
	})

	t.Run("path bytes", func(t *testing.T) {
		issue11AssertProtocolFailure(t, func(tree map[string]any) {
			issue11DatagramTree(t, tree)[strings.Repeat("p", issue11MaxUnknownPathBytes)] = true
		})
	})

	t.Run("per-value bytes", func(t *testing.T) {
		issue11AssertProtocolFailure(t, func(tree map[string]any) {
			issue11CommandTree(t, tree)["largeExtension"] =
				strings.Repeat("v", issue11MaxUnknownValueBytes)
		})
	})

	t.Run("total bytes", func(t *testing.T) {
		issue11AssertProtocolFailure(t, func(tree map[string]any) {
			datagram := issue11DatagramTree(t, tree)
			value := strings.Repeat("t", issue11MaxUnknownValueBytes-2)
			fieldCount := issue11MaxUnknownTotalBytes/issue11MaxUnknownValueBytes + 1
			for index := 0; index < fieldCount; index++ {
				datagram[fmt.Sprintf("totalExtension%02d", index)] = value
			}
		})
	})
}

func TestIssue11MalformedOriginalResponseCompletesWithProtocolError(t *testing.T) {
	writer := newCorrelatedWriteRecorder()
	fixture := newCorrelatedFixture(t, writer, "issue11-malformed")
	result := startCorrelatedRoundTrip(
		context.Background(),
		fixture.roundTripper,
		fixture.request,
	)
	request := writer.next(t)
	message := correlatedResponse(
		request,
		model.CmdClassifierTypeReply,
		[]model.CmdType{fixture.reply},
	)
	message = append(message, '{')
	_, _ = fixture.remote.HandleSpineMesssage(message)

	got := receiveCorrelatedResult(t, result)
	var protocolErr *api.CorrelatedProtocolError
	if !errors.As(got.err, &protocolErr) {
		t.Fatalf("RoundTrip() error = %T %v, want CorrelatedProtocolError", got.err, got.err)
	}
	if fixture.roundTripper.Stats().InFlight != 0 {
		t.Fatalf("in-flight count after malformed response = %d, want 0", fixture.roundTripper.Stats().InFlight)
	}
}

func TestIssue11IntermediateReadAckPreparationFailureIsTerminal(t *testing.T) {
	writer := newCorrelatedWriteRecorder()
	fixture := newCorrelatedFixture(t, writer, "issue11-read-ack-preflight")
	fixture.request.AckRequest = true
	result := startCorrelatedRoundTrip(
		context.Background(),
		fixture.roundTripper,
		fixture.request,
	)
	request := writer.next(t)
	message := issue11MessageWithMutation(
		t,
		correlatedResponse(
			request,
			model.CmdClassifierTypeResult,
			[]model.CmdType{{
				ResultData: &model.ResultDataType{
					ErrorNumber: util.Ptr(model.ErrorNumberTypeNoError),
				},
			}},
		),
		func(tree map[string]any) {
			datagram := issue11DatagramTree(t, tree)
			for index := 0; index <= issue11MaxUnknownFields; index++ {
				datagram[fmt.Sprintf("ackExtension%03d", index)] = index
			}
		},
	)
	_, _ = fixture.remote.HandleSpineMesssage(message)

	got := receiveCorrelatedResult(t, result)
	var protocolErr *api.CorrelatedProtocolError
	if !errors.As(got.err, &protocolErr) {
		t.Fatalf("RoundTrip() error = %T %v, want CorrelatedProtocolError", got.err, got.err)
	}
	if fixture.roundTripper.Stats().InFlight != 0 {
		t.Fatalf("in-flight count after malformed READ ACK = %d, want 0", fixture.roundTripper.Stats().InFlight)
	}
}

func TestIssue11MalformedPrimaryResponseUsesValidatedHeaderIdentity(t *testing.T) {
	t.Run("matching header completes its waiter", func(t *testing.T) {
		writer := newCorrelatedWriteRecorder()
		fixture := newCorrelatedFixture(t, writer, "issue11-malformed-primary")
		result := startCorrelatedRoundTrip(
			context.Background(),
			fixture.roundTripper,
			fixture.request,
		)
		request := writer.next(t)
		message := correlatedResponse(
			request,
			model.CmdClassifierTypeReply,
			[]model.CmdType{fixture.reply},
		)
		message = issue11ReplaceOnce(
			t,
			message,
			[]byte(`"brandName":"roundtrip"`),
			[]byte(`"brandName":[`),
		)
		_, _ = fixture.remote.HandleSpineMesssage(message)

		got := receiveCorrelatedResult(t, result)
		var protocolErr *api.CorrelatedProtocolError
		if !errors.As(got.err, &protocolErr) {
			t.Fatalf("RoundTrip() error = %T %v, want CorrelatedProtocolError", got.err, got.err)
		}
		if fixture.roundTripper.Stats().InFlight != 0 {
			t.Fatalf("in-flight count after malformed primary response = %d, want 0", fixture.roundTripper.Stats().InFlight)
		}
	})

	t.Run("mismatched header cannot target another waiter", func(t *testing.T) {
		writer := newCorrelatedWriteRecorder()
		fixture := newCorrelatedFixture(t, writer, "issue11-malformed-target")
		firstResult := startCorrelatedRoundTrip(
			context.Background(),
			fixture.roundTripper,
			fixture.request,
		)
		firstRequest := writer.next(t)

		secondRequestSpec := fixture.request
		secondDevice := model.AddressDeviceType("remote-issue11-other")
		secondRequestSpec.Destination.Device = &secondDevice
		secondResult := startCorrelatedRoundTrip(
			context.Background(),
			fixture.roundTripper,
			secondRequestSpec,
		)
		secondRequest := writer.next(t)

		message := correlatedResponse(
			firstRequest,
			model.CmdClassifierTypeReply,
			[]model.CmdType{fixture.reply},
		)
		firstReference := []byte(fmt.Sprintf(
			`"msgCounterReference":%d`,
			*firstRequest.Header.MsgCounter,
		))
		secondReference := []byte(fmt.Sprintf(
			`"msgCounterReference":%d`,
			*secondRequest.Header.MsgCounter,
		))
		message = issue11ReplaceOnce(t, message, firstReference, secondReference)
		message = issue11ReplaceOnce(
			t,
			message,
			[]byte(`"brandName":"roundtrip"`),
			[]byte(`"brandName":[`),
		)
		_, _ = fixture.remote.HandleSpineMesssage(message)

		assertNoCorrelatedResult(t, firstResult)
		assertNoCorrelatedResult(t, secondResult)
		if got := fixture.roundTripper.Stats().InFlight; got != 2 {
			t.Fatalf("in-flight count after mismatched malformed header = %d, want 2", got)
		}

		_, _ = fixture.remote.HandleSpineMesssage(
			correlatedResponse(
				firstRequest,
				model.CmdClassifierTypeReply,
				[]model.CmdType{fixture.reply},
			),
		)
		if got := receiveCorrelatedResult(t, firstResult); got.err != nil {
			t.Fatalf("first RoundTrip() cleanup error = %v", got.err)
		}

		_, _ = fixture.remote.HandleSpineMesssage(
			correlatedResponse(
				secondRequest,
				model.CmdClassifierTypeReply,
				[]model.CmdType{fixture.reply},
			),
		)
		if got := receiveCorrelatedResult(t, secondResult); got.err != nil {
			t.Fatalf("second RoundTrip() cleanup error = %v", got.err)
		}
	})
}
