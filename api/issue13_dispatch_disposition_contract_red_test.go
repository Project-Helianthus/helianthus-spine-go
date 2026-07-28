package api

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type issue13RoundTripSignature interface {
	RoundTrip(context.Context, CorrelatedRequest) (CorrelatedResponse, error)
}

var (
	_ error                     = (*CorrelatedRoundTripError)(nil)
	_ issue13RoundTripSignature = (CorrelatedRoundTripper)(nil)
	_ DispatchDisposition       = NoTransportHandoff
	_ DispatchDisposition       = TransportHandoffPossible
)

func TestIssue13DispatchDispositionContract(t *testing.T) {
	if NoTransportHandoff == TransportHandoffPossible {
		t.Fatal("dispatch dispositions collapse no handoff and possible handoff")
	}

	dispositionType := reflect.TypeOf(NoTransportHandoff)
	if dispositionType.Name() != "DispatchDisposition" {
		t.Fatalf("disposition type name = %q, want DispatchDisposition", dispositionType.Name())
	}

	errorType := reflect.TypeOf(CorrelatedRoundTripError{})
	causeField, ok := errorType.FieldByName("Cause")
	if !ok || causeField.Type != reflect.TypeOf((*error)(nil)).Elem() {
		t.Fatalf("CorrelatedRoundTripError.Cause = %+v, want error", causeField)
	}
	dispositionField, ok := errorType.FieldByName("Disposition")
	if !ok || dispositionField.Type != dispositionType {
		t.Fatalf(
			"CorrelatedRoundTripError.Disposition = %+v, want DispatchDisposition",
			dispositionField,
		)
	}
}

func TestIssue13CorrelatedRoundTripErrorUnwrapPreservesIsAndAs(t *testing.T) {
	root := errors.New("issue13 root cause")
	protocolCause := &CorrelatedProtocolError{
		Message: "issue13 protocol failure",
		Cause:   root,
	}
	err := &CorrelatedRoundTripError{
		Cause:       protocolCause,
		Disposition: TransportHandoffPossible,
	}

	if got := errors.Unwrap(err); got != protocolCause {
		t.Fatalf("errors.Unwrap() = %T %v, want exact protocol cause", got, got)
	}
	if !errors.Is(err, root) {
		t.Fatalf("errors.Is(%v, root) = false", err)
	}

	var gotRoundTrip *CorrelatedRoundTripError
	if !errors.As(err, &gotRoundTrip) || gotRoundTrip != err {
		t.Fatalf("errors.As() round-trip error = %p, want %p", gotRoundTrip, err)
	}
	var gotProtocol *CorrelatedProtocolError
	if !errors.As(err, &gotProtocol) || gotProtocol != protocolCause {
		t.Fatalf("errors.As() protocol error = %p, want %p", gotProtocol, protocolCause)
	}
	if !strings.Contains(err.Error(), protocolCause.Error()) {
		t.Fatalf("Error() = %q, want wrapped cause text %q", err.Error(), protocolCause.Error())
	}
}
