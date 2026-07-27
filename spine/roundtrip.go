package spine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/Project-Helianthus/helianthus-spine-go/api"
	"github.com/Project-Helianthus/helianthus-spine-go/model"
)

const (
	maxPendingCorrelatedRoundTrips = 16
	maxCorrelatedTombstones        = 64
)

type correlatedRoundTripOutcome struct {
	response api.CorrelatedResponse
	err      error
}

type pendingCorrelatedRoundTrip struct {
	request api.CorrelatedRequest
	result  chan correlatedRoundTripOutcome
}

var _ api.CorrelatedRoundTripper = (*Sender)(nil)

func (c *Sender) RoundTrip(ctx context.Context, request api.CorrelatedRequest) (api.CorrelatedResponse, error) {
	if ctx == nil {
		return api.CorrelatedResponse{}, errors.New("correlated round-trip context is nil")
	}
	if err := ctx.Err(); err != nil {
		return api.CorrelatedResponse{}, err
	}

	request, err := cloneAndValidateCorrelatedRequest(request)
	if err != nil {
		return api.CorrelatedResponse{}, err
	}

	pending := &pendingCorrelatedRoundTrip{
		request: request,
		result:  make(chan correlatedRoundTripOutcome, 1),
	}

	key, err := c.registerCorrelatedRoundTrip(pending)
	if err != nil {
		return api.CorrelatedResponse{}, err
	}

	datagram := model.DatagramType{
		Header: model.HeaderType{
			SpecificationVersion: &SpecificationVersion,
			AddressSource:        &pending.request.Source,
			AddressDestination:   &pending.request.Destination,
			MsgCounter:           &key,
			CmdClassifier:        &pending.request.Classifier,
		},
		Payload: model.PayloadType{
			Cmd: []model.CmdType{pending.request.Cmd},
		},
	}
	if pending.request.AckRequest {
		datagram.Header.AckRequest = &pending.request.AckRequest
	}

	if sendErr := c.sendSpineMessage(datagram); sendErr != nil {
		if c.finishCorrelatedRoundTrip(key, correlatedRoundTripOutcome{err: sendErr}) {
			return api.CorrelatedResponse{}, sendErr
		}
		outcome := <-pending.result
		return outcome.response, outcome.err
	}

	select {
	case outcome := <-pending.result:
		return outcome.response, outcome.err
	case <-ctx.Done():
		if c.finishCorrelatedRoundTrip(key, correlatedRoundTripOutcome{err: ctx.Err()}) {
			return api.CorrelatedResponse{}, ctx.Err()
		}
		outcome := <-pending.result
		return outcome.response, outcome.err
	}
}

func (c *Sender) Stats() api.CorrelatedRoundTripStats {
	c.roundTripMux.Lock()
	defer c.roundTripMux.Unlock()

	return api.CorrelatedRoundTripStats{
		InFlight:          len(c.pendingRoundTrips),
		Capacity:          maxPendingCorrelatedRoundTrips,
		Tombstones:        len(c.roundTripTombstones),
		TombstoneCapacity: maxCorrelatedTombstones,
		HighWatermark:     c.retiredHighWatermark,
		Closed:            c.roundTripsClosed,
		Exhausted:         c.roundTripExhausted || c.messageCounterWrapped.Load(),
	}
}

func (c *Sender) Close() error {
	c.roundTripCloseMux.Lock()
	defer c.roundTripCloseMux.Unlock()

	c.roundTripMux.Lock()
	if c.roundTripsClosed {
		c.roundTripMux.Unlock()
		return nil
	}
	c.roundTripsRetiring = true
	c.ensureRoundTripIdleLocked()
	for c.activeRoundTripSends > 0 || c.activeRoundTripReads > 0 {
		c.roundTripIdle.Wait()
	}
	c.roundTripsClosed = true

	type retiredOperation struct {
		key       model.MsgCounterType
		operation *pendingCorrelatedRoundTrip
	}
	retired := make([]retiredOperation, 0, len(c.pendingRoundTrips))
	for key, operation := range c.pendingRoundTrips {
		retired = append(retired, retiredOperation{key: key, operation: operation})
		delete(c.pendingRoundTrips, key)
		c.retireCorrelatedKeyLocked(key)
	}
	c.roundTripMux.Unlock()

	sort.Slice(retired, func(i, j int) bool { return retired[i].key < retired[j].key })
	for _, item := range retired {
		item.operation.result <- correlatedRoundTripOutcome{err: api.ErrCorrelatedRoundTripClosed}
	}

	return nil
}

func (c *Sender) ensureRoundTripIdleLocked() {
	if c.roundTripIdle == nil {
		c.roundTripIdle = sync.NewCond(&c.roundTripMux)
	}
}

func (c *Sender) beginSpineSend() error {
	c.roundTripMux.Lock()
	defer c.roundTripMux.Unlock()

	if c.roundTripsClosed || c.roundTripsRetiring {
		return api.ErrCorrelatedRoundTripClosed
	}
	c.activeRoundTripSends++
	return nil
}

func (c *Sender) endSpineSend() {
	c.roundTripMux.Lock()
	c.activeRoundTripSends--
	if c.activeRoundTripSends == 0 {
		c.ensureRoundTripIdleLocked()
		c.roundTripIdle.Broadcast()
	}
	c.roundTripMux.Unlock()
}

func (c *Sender) beginIncomingSpineMessage() bool {
	c.roundTripMux.Lock()
	defer c.roundTripMux.Unlock()

	if c.roundTripsClosed || (c.roundTripsRetiring && c.activeRoundTripSends == 0) {
		return false
	}
	c.activeRoundTripReads++
	return true
}

func (c *Sender) endIncomingSpineMessage() {
	c.roundTripMux.Lock()
	c.activeRoundTripReads--
	if c.activeRoundTripReads == 0 {
		c.ensureRoundTripIdleLocked()
		c.roundTripIdle.Broadcast()
	}
	c.roundTripMux.Unlock()
}

func cloneAndValidateCorrelatedRequest(request api.CorrelatedRequest) (api.CorrelatedRequest, error) {
	switch request.Classifier {
	case model.CmdClassifierTypeRead, model.CmdClassifierTypeWrite:
	default:
		return api.CorrelatedRequest{}, fmt.Errorf(
			"unsupported correlated request classifier %q",
			request.Classifier,
		)
	}
	if err := validateCorrelatedFeatureAddress("source", request.Source); err != nil {
		return api.CorrelatedRequest{}, err
	}
	if err := validateCorrelatedFeatureAddress("destination", request.Destination); err != nil {
		return api.CorrelatedRequest{}, err
	}
	if _, err := request.Cmd.Data(); err != nil && request.Cmd.Function == nil {
		return api.CorrelatedRequest{}, &api.CorrelatedProtocolError{
			Message: "correlated request command is empty",
			Cause:   err,
		}
	}

	request.Source = cloneCorrelatedFeatureAddress(request.Source)
	request.Destination = cloneCorrelatedFeatureAddress(request.Destination)

	data, err := json.Marshal(request.Cmd)
	if err != nil {
		return api.CorrelatedRequest{}, &api.CorrelatedProtocolError{
			Message: "encode correlated request command",
			Cause:   err,
		}
	}
	var cmd model.CmdType
	if err := json.Unmarshal(data, &cmd); err != nil {
		return api.CorrelatedRequest{}, &api.CorrelatedProtocolError{
			Message: "clone correlated request command",
			Cause:   err,
		}
	}
	request.Cmd = cmd

	return request, nil
}

func validateCorrelatedFeatureAddress(name string, address model.FeatureAddressType) error {
	if address.Device == nil || len(*address.Device) == 0 {
		return fmt.Errorf("correlated request %s device address is required", name)
	}
	if address.Entity == nil {
		return fmt.Errorf("correlated request %s entity address is required", name)
	}
	if address.Feature == nil {
		return fmt.Errorf("correlated request %s feature address is required", name)
	}
	return nil
}

func cloneCorrelatedFeatureAddress(address model.FeatureAddressType) model.FeatureAddressType {
	result := address
	if address.Device != nil {
		device := *address.Device
		result.Device = &device
	}
	result.Entity = append([]model.AddressEntityType(nil), address.Entity...)
	if address.Feature != nil {
		feature := *address.Feature
		result.Feature = &feature
	}
	return result
}

func (c *Sender) registerCorrelatedRoundTrip(pending *pendingCorrelatedRoundTrip) (model.MsgCounterType, error) {
	c.roundTripMux.Lock()
	defer c.roundTripMux.Unlock()

	if c.roundTripsClosed || c.roundTripsRetiring {
		return 0, api.ErrCorrelatedRoundTripClosed
	}
	if len(c.pendingRoundTrips) >= maxPendingCorrelatedRoundTrips {
		return 0, api.ErrCorrelatedRoundTripCapacity
	}
	if c.messageCounterWrapped.Load() {
		c.roundTripExhausted = true
		return 0, api.ErrCorrelatedCounterExhausted
	}
	if c.pendingRoundTrips == nil {
		c.pendingRoundTrips = make(map[model.MsgCounterType]*pendingCorrelatedRoundTrip)
	}

	for {
		current := atomic.LoadUint64(&c.msgNum)
		if current == math.MaxUint64 {
			c.roundTripExhausted = true
			return 0, api.ErrCorrelatedCounterExhausted
		}
		next := current + 1
		if !atomic.CompareAndSwapUint64(&c.msgNum, current, next) {
			continue
		}

		key := model.MsgCounterType(next)
		if _, exists := c.pendingRoundTrips[key]; exists {
			return 0, api.ErrCorrelatedKeyInFlight
		}
		if key <= c.retiredHighWatermark {
			return 0, api.ErrCorrelatedKeyRetired
		}

		c.pendingRoundTrips[key] = pending
		return key, nil
	}
}

func (c *Sender) finishCorrelatedRoundTrip(
	key model.MsgCounterType,
	outcome correlatedRoundTripOutcome,
) bool {
	c.roundTripMux.Lock()
	pending, exists := c.pendingRoundTrips[key]
	if !exists {
		c.roundTripMux.Unlock()
		return false
	}
	delete(c.pendingRoundTrips, key)
	c.retireCorrelatedKeyLocked(key)
	c.roundTripMux.Unlock()

	pending.result <- outcome
	return true
}

func (c *Sender) retireCorrelatedKeyLocked(key model.MsgCounterType) {
	if key > c.retiredHighWatermark {
		c.retiredHighWatermark = key
	}
	if len(c.roundTripTombstones) == maxCorrelatedTombstones {
		copy(c.roundTripTombstones, c.roundTripTombstones[1:])
		c.roundTripTombstones[len(c.roundTripTombstones)-1] = key
		return
	}
	c.roundTripTombstones = append(c.roundTripTombstones, key)
}

func (c *Sender) completeCorrelatedResponse(datagram model.DatagramType, processErr error) bool {
	reference := datagram.Header.MsgCounterReference
	if reference == nil {
		return false
	}

	c.roundTripMux.Lock()
	pending, exists := c.pendingRoundTrips[*reference]
	c.roundTripMux.Unlock()
	if !exists {
		return false
	}

	outcome, terminal := classifyCorrelatedResponse(*reference, pending.request, datagram, processErr)
	if !terminal {
		return false
	}
	return c.finishCorrelatedRoundTrip(*reference, outcome)
}

func (c *Sender) correlatedResponsePreflightError(datagram model.DatagramType) error {
	reference := datagram.Header.MsgCounterReference
	if reference == nil {
		return nil
	}

	c.roundTripMux.Lock()
	_, pending := c.pendingRoundTrips[*reference]
	c.roundTripMux.Unlock()
	if !pending {
		return nil
	}

	if datagram.Header.AddressSource == nil {
		return errors.New("correlated response source address is missing")
	}
	if datagram.Header.AddressDestination == nil {
		return errors.New("correlated response destination address is missing")
	}
	if datagram.Header.CmdClassifier != nil &&
		*datagram.Header.CmdClassifier == model.CmdClassifierTypeResult &&
		len(datagram.Payload.Cmd) > 0 &&
		(datagram.Payload.Cmd[0].ResultData == nil ||
			datagram.Payload.Cmd[0].ResultData.ErrorNumber == nil) {
		return errors.New("correlated result data or error number is missing")
	}
	return nil
}

func classifyCorrelatedResponse(
	key model.MsgCounterType,
	request api.CorrelatedRequest,
	datagram model.DatagramType,
	processErr error,
) (correlatedRoundTripOutcome, bool) {
	protocolError := func(message string, cause error) (correlatedRoundTripOutcome, bool) {
		return correlatedRoundTripOutcome{
			err: &api.CorrelatedProtocolError{
				Message: message,
				Cause:   cause,
			},
		}, true
	}

	if !reflect.DeepEqual(datagram.Header.AddressSource, &request.Destination) ||
		!reflect.DeepEqual(datagram.Header.AddressDestination, &request.Source) {
		return protocolError("correlated response address mismatch", processErr)
	}
	if datagram.Header.CmdClassifier == nil {
		return protocolError("correlated response classifier is missing", processErr)
	}
	if len(datagram.Payload.Cmd) != 1 {
		return protocolError(
			fmt.Sprintf("correlated response contains %d commands, want exactly one", len(datagram.Payload.Cmd)),
			processErr,
		)
	}

	cmd := datagram.Payload.Cmd[0]
	response := api.CorrelatedResponse{
		CorrelationKey: key,
		Header:         datagram.Header,
		Cmd:            cmd,
	}

	switch *datagram.Header.CmdClassifier {
	case model.CmdClassifierTypeReply:
		if request.Classifier != model.CmdClassifierTypeRead {
			return protocolError("correlated WRITE received a reply instead of a result", processErr)
		}
		if processErr != nil {
			return protocolError("correlated reply processing failed", processErr)
		}
		return correlatedRoundTripOutcome{response: response}, true

	case model.CmdClassifierTypeResult:
		if cmd.ResultData == nil || cmd.ResultData.ErrorNumber == nil {
			return protocolError("correlated result data or error number is missing", processErr)
		}
		if *cmd.ResultData.ErrorNumber != model.ErrorNumberTypeNoError {
			return correlatedRoundTripOutcome{
				response: response,
				err: &api.CorrelatedRemoteError{
					ErrorNumber: *cmd.ResultData.ErrorNumber,
					Description: cmd.ResultData.Description,
				},
			}, true
		}
		if request.Classifier == model.CmdClassifierTypeRead {
			if request.AckRequest {
				return correlatedRoundTripOutcome{}, false
			}
			return protocolError("correlated READ received a no-error result without reply data", processErr)
		}
		if processErr != nil {
			return protocolError("correlated result processing failed", processErr)
		}
		return correlatedRoundTripOutcome{response: response}, true

	default:
		return protocolError(
			fmt.Sprintf("unexpected correlated response classifier %q", *datagram.Header.CmdClassifier),
			processErr,
		)
	}
}
