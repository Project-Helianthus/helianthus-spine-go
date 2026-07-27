package api

import (
	"reflect"
	"strings"
	"testing"
)

func TestIssue11CorrelatedResponseUnknownFieldCarrierContract(t *testing.T) {
	responseType := reflect.TypeOf(CorrelatedResponse{})
	unknowns, ok := responseType.FieldByName("UnknownFields")
	if !ok {
		t.Fatal("CorrelatedResponse.UnknownFields is missing; correlated replies discard unknown JSON members")
	}
	if unknowns.Type.Kind() != reflect.Slice {
		t.Fatalf("CorrelatedResponse.UnknownFields type = %s, want slice", unknowns.Type)
	}

	observationType := unknowns.Type.Elem()
	if observationType.Kind() != reflect.Struct {
		t.Fatalf("unknown field element type = %s, want struct", observationType)
	}
	if observationType.NumField() != 2 {
		t.Fatalf("unknown field exported shape has %d fields, want Path and Value", observationType.NumField())
	}

	path, ok := observationType.FieldByName("Path")
	if !ok || path.Type.Kind() != reflect.String {
		t.Fatalf("unknown field Path = %+v, want string", path)
	}
	value, ok := observationType.FieldByName("Value")
	if !ok || value.Type.Name() != "CorrelatedUnknownValue" ||
		value.Type.Kind() != reflect.Slice ||
		value.Type.Elem().Kind() != reflect.Uint8 {
		t.Fatalf("unknown field Value = %+v, want byte-backed CorrelatedUnknownValue", value)
	}

	for _, forbidden := range []string{
		"Raw", "RawMessage", "Message", "Frame", "Payload", "Transcript", "Bytes",
	} {
		if _, exists := responseType.FieldByName(forbidden); exists {
			t.Errorf("CorrelatedResponse exposes forbidden whole-payload field %q", forbidden)
		}
	}

	source := reflect.TypeOf(CorrelatedResponse{})
	for index := 0; index < source.NumField(); index++ {
		name := strings.ToLower(source.Field(index).Name)
		if name == "unknownfields" {
			continue
		}
		if strings.Contains(name, "raw") ||
			strings.Contains(name, "frame") ||
			strings.Contains(name, "transcript") ||
			strings.Contains(name, "bytes") {
			t.Errorf("CorrelatedResponse field %q widens the whole-payload boundary", source.Field(index).Name)
		}
	}
}
