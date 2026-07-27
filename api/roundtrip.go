package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/Project-Helianthus/helianthus-spine-go/model"
)

var (
	ErrCorrelatedRoundTripClosed   = errors.New("sender connection generation is closed")
	ErrCorrelatedRoundTripCapacity = errors.New("correlated round-trip capacity reached")
	ErrCorrelatedCounterExhausted  = errors.New("correlated message counter exhausted")
	ErrCorrelatedKeyInFlight       = errors.New("correlated message counter already in flight")
	ErrCorrelatedKeyRetired        = errors.New("correlated message counter already retired")
)

// CorrelatedRequest describes one full SPINE request. Cmd is encoded as the
// sole command in the datagram payload.
type CorrelatedRequest struct {
	Classifier  model.CmdClassifierType
	Source      model.FeatureAddressType
	Destination model.FeatureAddressType
	AckRequest  bool
	Cmd         model.CmdType
}

// CorrelatedResponse is the typed reply or successful result associated with
// one CorrelatedRequest.
type CorrelatedResponse struct {
	CorrelationKey model.MsgCounterType
	Header         model.HeaderType
	Cmd            model.CmdType
}

// CorrelatedRemoteError reports a correlated SPINE result with a non-zero
// protocol error number.
type CorrelatedRemoteError struct {
	ErrorNumber model.ErrorNumberType
	Description *model.DescriptionType
}

func (e *CorrelatedRemoteError) Error() string {
	if e == nil {
		return "remote SPINE error"
	}
	if e.Description != nil && len(*e.Description) > 0 {
		return fmt.Sprintf("remote SPINE error %d: %s", e.ErrorNumber, *e.Description)
	}
	return fmt.Sprintf("remote SPINE error %d", e.ErrorNumber)
}

// CorrelatedProtocolError reports a correlated response that could not be
// admitted as a valid typed reply or result.
type CorrelatedProtocolError struct {
	Message string
	Cause   error
}

func (e *CorrelatedProtocolError) Error() string {
	if e == nil {
		return "invalid correlated SPINE response"
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Cause)
	}
	return e.Message
}

func (e *CorrelatedProtocolError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// CorrelatedRoundTripStats contains bounded, payload-free state for one
// Sender connection generation.
type CorrelatedRoundTripStats struct {
	InFlight          int
	Capacity          int
	Tombstones        int
	TombstoneCapacity int
	HighWatermark     model.MsgCounterType
	Closed            bool
	Exhausted         bool
}

// CorrelatedRoundTripper owns request correlation for one Sender connection
// generation. Close retires all pending operations and rejects new ones.
type CorrelatedRoundTripper interface {
	RoundTrip(ctx context.Context, request CorrelatedRequest) (CorrelatedResponse, error)
	Stats() CorrelatedRoundTripStats
	Close() error
}
