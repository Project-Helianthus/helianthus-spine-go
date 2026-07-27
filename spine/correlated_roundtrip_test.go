package spine

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-spine-go/api"
	"github.com/Project-Helianthus/helianthus-spine-go/model"
	"github.com/Project-Helianthus/helianthus-spine-go/util"
)

const correlatedTestTimeout = 2 * time.Second

type correlatedWriteRecorder struct {
	mu      sync.Mutex
	writes  []model.DatagramType
	written chan model.DatagramType
	onWrite func(model.DatagramType)
}

func newCorrelatedWriteRecorder() *correlatedWriteRecorder {
	return &correlatedWriteRecorder{
		written: make(chan model.DatagramType, 128),
	}
}

func (w *correlatedWriteRecorder) WriteShipMessageWithPayload(message []byte) {
	var envelope model.Datagram
	if err := json.Unmarshal(message, &envelope); err != nil {
		panic(err)
	}

	datagram := envelope.Datagram
	w.mu.Lock()
	w.writes = append(w.writes, datagram)
	onWrite := w.onWrite
	w.mu.Unlock()

	if onWrite != nil {
		onWrite(datagram)
		return
	}
	w.written <- datagram
}

func (w *correlatedWriteRecorder) next(t *testing.T) model.DatagramType {
	t.Helper()
	select {
	case datagram := <-w.written:
		return datagram
	case <-time.After(correlatedTestTimeout):
		t.Fatal("timed out waiting for outgoing datagram")
		return model.DatagramType{}
	}
}

func (w *correlatedWriteRecorder) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.writes)
}

type correlatedFixture struct {
	local         *DeviceLocal
	localFeature  *FeatureLocal
	remote        *DeviceRemote
	remoteFeature *FeatureRemote
	sender        *Sender
	roundTripper  api.CorrelatedRoundTripper
	request       api.CorrelatedRequest
	reply         model.CmdType
}

func newCorrelatedFixture(t *testing.T, writer *correlatedWriteRecorder, ski string) *correlatedFixture {
	t.Helper()

	local := NewDeviceLocal(
		"brand",
		"model",
		"serial",
		"code",
		"local",
		model.DeviceTypeTypeEnergyManagementSystem,
		model.NetworkManagementFeatureSetTypeSmart,
	)
	localEntity := NewEntityLocal(
		local,
		model.EntityTypeTypeDeviceInformation,
		[]model.AddressEntityType{1},
		time.Second,
	)
	localFeature := NewFeatureLocal(
		1,
		localEntity,
		model.FeatureTypeTypeDeviceClassification,
		model.RoleTypeClient,
	)
	localEntity.AddFeature(localFeature)
	local.AddEntity(localEntity)

	senderI := NewSender(writer)
	sender, ok := senderI.(*Sender)
	if !ok {
		t.Fatalf("NewSender() type = %T, want *Sender", senderI)
	}
	roundTripper, ok := senderI.(api.CorrelatedRoundTripper)
	if !ok {
		t.Fatalf("NewSender() does not implement api.CorrelatedRoundTripper")
	}

	remote := NewDeviceRemote(local, ski, sender)
	remote.address = util.Ptr(model.AddressDeviceType("remote-" + ski))
	remoteEntity := NewEntityRemote(
		remote,
		model.EntityTypeTypeDeviceInformation,
		[]model.AddressEntityType{1},
	)
	remoteFeature := NewFeatureRemote(
		1,
		remoteEntity,
		model.FeatureTypeTypeDeviceClassification,
		model.RoleTypeServer,
	)
	remoteEntity.AddFeature(remoteFeature)
	remote.AddEntity(remoteEntity)
	local.AddRemoteDeviceForSki(ski, remote)

	function := model.FunctionTypeDeviceClassificationManufacturerData
	request := api.CorrelatedRequest{
		Classifier:  model.CmdClassifierTypeRead,
		Source:      *localFeature.Address(),
		Destination: *remoteFeature.Address(),
		AckRequest:  false,
		Cmd: model.CmdType{
			Function: &function,
		},
	}
	reply := model.CmdType{
		DeviceClassificationManufacturerData: &model.DeviceClassificationManufacturerDataType{
			BrandName: util.Ptr(model.DeviceClassificationStringType("roundtrip")),
		},
	}

	return &correlatedFixture{
		local:         local,
		localFeature:  localFeature,
		remote:        remote,
		remoteFeature: remoteFeature,
		sender:        sender,
		roundTripper:  roundTripper,
		request:       request,
		reply:         reply,
	}
}

type correlatedEventCounter struct {
	count atomic.Int32
}

func (c *correlatedEventCounter) HandleEvent(api.EventPayload) {
	c.count.Add(1)
}

func correlatedResponse(
	request model.DatagramType,
	classifier model.CmdClassifierType,
	cmds []model.CmdType,
) []byte {
	responseCounter := model.MsgCounterType(9000)
	envelope := model.Datagram{
		Datagram: model.DatagramType{
			Header: model.HeaderType{
				SpecificationVersion: &SpecificationVersion,
				AddressSource:        request.Header.AddressDestination,
				AddressDestination:   request.Header.AddressSource,
				MsgCounter:           &responseCounter,
				MsgCounterReference:  request.Header.MsgCounter,
				CmdClassifier:        &classifier,
			},
			Payload: model.PayloadType{Cmd: cmds},
		},
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		panic(err)
	}
	return data
}

type correlatedResult struct {
	response api.CorrelatedResponse
	err      error
}

func startCorrelatedRoundTrip(
	ctx context.Context,
	roundTripper api.CorrelatedRoundTripper,
	request api.CorrelatedRequest,
) <-chan correlatedResult {
	result := make(chan correlatedResult, 1)
	go func() {
		response, err := roundTripper.RoundTrip(ctx, request)
		result <- correlatedResult{response: response, err: err}
	}()
	return result
}

func receiveCorrelatedResult(t *testing.T, result <-chan correlatedResult) correlatedResult {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(correlatedTestTimeout):
		t.Fatal("timed out waiting for correlated round trip")
		return correlatedResult{}
	}
}

func assertNoCorrelatedResult(t *testing.T, result <-chan correlatedResult) {
	t.Helper()
	select {
	case value := <-result:
		t.Fatalf("round trip completed unexpectedly: response=%+v err=%v", value.response, value.err)
	case <-time.After(25 * time.Millisecond):
	}
}

func waitForInFlight(t *testing.T, roundTripper api.CorrelatedRoundTripper, want int) {
	t.Helper()
	deadline := time.Now().Add(correlatedTestTimeout)
	for time.Now().Before(deadline) {
		if roundTripper.Stats().InFlight == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("in-flight count = %d, want %d", roundTripper.Stats().InFlight, want)
}

func TestCorrelatedRoundTripSynchronousReplyRegistersBeforeSend(t *testing.T) {
	writer := newCorrelatedWriteRecorder()
	fixture := newCorrelatedFixture(t, writer, "sync")

	writer.onWrite = func(request model.DatagramType) {
		if _, err := fixture.remote.HandleSpineMesssage(
			correlatedResponse(request, model.CmdClassifierTypeReply, []model.CmdType{fixture.reply}),
		); err != nil {
			t.Errorf("HandleSpineMesssage() error = %v", err)
		}
	}

	response, err := fixture.roundTripper.RoundTrip(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	if response.CorrelationKey == 0 {
		t.Fatal("RoundTrip() returned an empty correlation key")
	}
	if response.Cmd.DeviceClassificationManufacturerData == nil {
		t.Fatalf("RoundTrip() response cmd = %+v, want manufacturer data", response.Cmd)
	}
	if got := fixture.roundTripper.Stats().InFlight; got != 0 {
		t.Fatalf("in-flight count after success = %d, want 0", got)
	}
}

func TestCorrelatedRoundTripCleanupTerminalPaths(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		writer := newCorrelatedWriteRecorder()
		fixture := newCorrelatedFixture(t, writer, "cleanup-success")
		result := startCorrelatedRoundTrip(context.Background(), fixture.roundTripper, fixture.request)
		request := writer.next(t)
		_, _ = fixture.remote.HandleSpineMesssage(
			correlatedResponse(request, model.CmdClassifierTypeReply, []model.CmdType{fixture.reply}),
		)
		if got := receiveCorrelatedResult(t, result); got.err != nil {
			t.Fatalf("RoundTrip() error = %v", got.err)
		}
		waitForInFlight(t, fixture.roundTripper, 0)
	})

	t.Run("local send failure", func(t *testing.T) {
		senderI := NewSender(nil)
		roundTripper := senderI.(api.CorrelatedRoundTripper)
		fixture := newCorrelatedFixture(t, newCorrelatedWriteRecorder(), "send-failure-shape")

		_, err := roundTripper.RoundTrip(context.Background(), fixture.request)
		if err == nil {
			t.Fatal("RoundTrip() error = nil, want local send error")
		}
		waitForInFlight(t, roundTripper, 0)
	})

	t.Run("deadline", func(t *testing.T) {
		writer := newCorrelatedWriteRecorder()
		fixture := newCorrelatedFixture(t, writer, "cleanup-deadline")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()

		_, err := fixture.roundTripper.RoundTrip(ctx, fixture.request)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("RoundTrip() error = %v, want deadline exceeded", err)
		}
		waitForInFlight(t, fixture.roundTripper, 0)
	})

	t.Run("cancel", func(t *testing.T) {
		writer := newCorrelatedWriteRecorder()
		fixture := newCorrelatedFixture(t, writer, "cleanup-cancel")
		ctx, cancel := context.WithCancel(context.Background())
		result := startCorrelatedRoundTrip(ctx, fixture.roundTripper, fixture.request)
		_ = writer.next(t)
		cancel()

		if got := receiveCorrelatedResult(t, result); !errors.Is(got.err, context.Canceled) {
			t.Fatalf("RoundTrip() error = %v, want context canceled", got.err)
		}
		waitForInFlight(t, fixture.roundTripper, 0)
	})

	t.Run("disconnect", func(t *testing.T) {
		writer := newCorrelatedWriteRecorder()
		fixture := newCorrelatedFixture(t, writer, "cleanup-disconnect")
		result := startCorrelatedRoundTrip(context.Background(), fixture.roundTripper, fixture.request)
		_ = writer.next(t)
		fixture.local.RemoveRemoteDeviceConnection(fixture.remote.Ski())

		if got := receiveCorrelatedResult(t, result); !errors.Is(got.err, api.ErrCorrelatedRoundTripClosed) {
			t.Fatalf("RoundTrip() error = %v, want sender closed", got.err)
		}
		waitForInFlight(t, fixture.roundTripper, 0)
	})

	t.Run("malformed correlated response", func(t *testing.T) {
		writer := newCorrelatedWriteRecorder()
		fixture := newCorrelatedFixture(t, writer, "cleanup-malformed")
		result := startCorrelatedRoundTrip(context.Background(), fixture.roundTripper, fixture.request)
		request := writer.next(t)
		_, _ = fixture.remote.HandleSpineMesssage(
			correlatedResponse(request, model.CmdClassifierTypeReply, nil),
		)

		got := receiveCorrelatedResult(t, result)
		var protocolErr *api.CorrelatedProtocolError
		if !errors.As(got.err, &protocolErr) {
			t.Fatalf("RoundTrip() error = %T %v, want CorrelatedProtocolError", got.err, got.err)
		}
		waitForInFlight(t, fixture.roundTripper, 0)
	})

	t.Run("remote error", func(t *testing.T) {
		writer := newCorrelatedWriteRecorder()
		fixture := newCorrelatedFixture(t, writer, "cleanup-remote-error")
		result := startCorrelatedRoundTrip(context.Background(), fixture.roundTripper, fixture.request)
		request := writer.next(t)
		resultCmd := model.CmdType{
			ResultData: &model.ResultDataType{
				ErrorNumber: util.Ptr(model.ErrorNumberTypeCommandRejected),
				Description: util.Ptr(model.DescriptionType("rejected")),
			},
		}
		_, _ = fixture.remote.HandleSpineMesssage(
			correlatedResponse(request, model.CmdClassifierTypeResult, []model.CmdType{resultCmd}),
		)

		got := receiveCorrelatedResult(t, result)
		var remoteErr *api.CorrelatedRemoteError
		if !errors.As(got.err, &remoteErr) {
			t.Fatalf("RoundTrip() error = %T %v, want CorrelatedRemoteError", got.err, got.err)
		}
		if remoteErr.ErrorNumber != model.ErrorNumberTypeCommandRejected {
			t.Fatalf("remote error number = %d, want %d", remoteErr.ErrorNumber, model.ErrorNumberTypeCommandRejected)
		}
		waitForInFlight(t, fixture.roundTripper, 0)
	})
}

func TestCorrelatedRoundTripLateReplyCannotCompleteSuccessorOrABAReuse(t *testing.T) {
	writer := newCorrelatedWriteRecorder()
	fixture := newCorrelatedFixture(t, writer, "aba")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	firstResult := startCorrelatedRoundTrip(ctx, fixture.roundTripper, fixture.request)
	firstRequest := writer.next(t)
	first := receiveCorrelatedResult(t, firstResult)
	cancel()
	if !errors.Is(first.err, context.DeadlineExceeded) {
		t.Fatalf("first RoundTrip() error = %v, want deadline exceeded", first.err)
	}
	firstKey := *firstRequest.Header.MsgCounter

	atomic.StoreUint64(&fixture.sender.msgNum, uint64(firstKey-1))
	if _, err := fixture.roundTripper.RoundTrip(context.Background(), fixture.request); !errors.Is(err, api.ErrCorrelatedKeyRetired) {
		t.Fatalf("retired key reuse error = %v, want ErrCorrelatedKeyRetired", err)
	}
	atomic.StoreUint64(&fixture.sender.msgNum, uint64(firstKey))

	secondResult := startCorrelatedRoundTrip(context.Background(), fixture.roundTripper, fixture.request)
	secondRequest := writer.next(t)
	if *secondRequest.Header.MsgCounter == firstKey {
		t.Fatalf("successor reused correlation key %d", firstKey)
	}

	_, _ = fixture.remote.HandleSpineMesssage(
		correlatedResponse(firstRequest, model.CmdClassifierTypeReply, []model.CmdType{fixture.reply}),
	)
	assertNoCorrelatedResult(t, secondResult)

	_, _ = fixture.remote.HandleSpineMesssage(
		correlatedResponse(secondRequest, model.CmdClassifierTypeReply, []model.CmdType{fixture.reply}),
	)
	if got := receiveCorrelatedResult(t, secondResult); got.err != nil {
		t.Fatalf("successor RoundTrip() error = %v", got.err)
	}
}

func TestCorrelatedRoundTripDuplicateReplyCompletesExactlyOnce(t *testing.T) {
	writer := newCorrelatedWriteRecorder()
	fixture := newCorrelatedFixture(t, writer, "duplicate")
	result := startCorrelatedRoundTrip(context.Background(), fixture.roundTripper, fixture.request)
	request := writer.next(t)
	reply := correlatedResponse(request, model.CmdClassifierTypeReply, []model.CmdType{fixture.reply})

	_, _ = fixture.remote.HandleSpineMesssage(reply)
	if got := receiveCorrelatedResult(t, result); got.err != nil {
		t.Fatalf("RoundTrip() error = %v", got.err)
	}
	before := fixture.roundTripper.Stats()
	_, _ = fixture.remote.HandleSpineMesssage(reply)
	after := fixture.roundTripper.Stats()

	if after.InFlight != 0 {
		t.Fatalf("in-flight count after duplicate = %d, want 0", after.InFlight)
	}
	if after.Tombstones != before.Tombstones {
		t.Fatalf("duplicate reply changed tombstones from %d to %d", before.Tombstones, after.Tombstones)
	}
}

func TestCorrelatedRoundTripReadAckWaitsForReply(t *testing.T) {
	writer := newCorrelatedWriteRecorder()
	fixture := newCorrelatedFixture(t, writer, "read-ack")
	fixture.request.AckRequest = true
	result := startCorrelatedRoundTrip(context.Background(), fixture.roundTripper, fixture.request)
	request := writer.next(t)
	noError := model.CmdType{
		ResultData: &model.ResultDataType{ErrorNumber: util.Ptr(model.ErrorNumberTypeNoError)},
	}

	_, _ = fixture.remote.HandleSpineMesssage(
		correlatedResponse(request, model.CmdClassifierTypeResult, []model.CmdType{noError}),
	)
	assertNoCorrelatedResult(t, result)
	if got := fixture.roundTripper.Stats().InFlight; got != 1 {
		t.Fatalf("in-flight count after intermediate ACK = %d, want 1", got)
	}

	_, _ = fixture.remote.HandleSpineMesssage(
		correlatedResponse(request, model.CmdClassifierTypeReply, []model.CmdType{fixture.reply}),
	)
	if got := receiveCorrelatedResult(t, result); got.err != nil {
		t.Fatalf("RoundTrip() error = %v", got.err)
	}
}

func TestCorrelatedRoundTripReplyCancelRace(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		writer := newCorrelatedWriteRecorder()
		fixture := newCorrelatedFixture(t, writer, "race")
		ctx, cancel := context.WithCancel(context.Background())
		result := startCorrelatedRoundTrip(ctx, fixture.roundTripper, fixture.request)
		request := writer.next(t)

		start := make(chan struct{})
		var racers sync.WaitGroup
		racers.Add(2)
		go func() {
			defer racers.Done()
			<-start
			cancel()
		}()
		go func() {
			defer racers.Done()
			<-start
			_, _ = fixture.remote.HandleSpineMesssage(
				correlatedResponse(request, model.CmdClassifierTypeReply, []model.CmdType{fixture.reply}),
			)
		}()
		close(start)
		racers.Wait()

		got := receiveCorrelatedResult(t, result)
		if got.err != nil && !errors.Is(got.err, context.Canceled) {
			t.Fatalf("iteration %d: RoundTrip() error = %v, want success or cancellation", iteration, got.err)
		}
		waitForInFlight(t, fixture.roundTripper, 0)
	}
}

func TestCorrelatedRoundTripBoundedInFlight(t *testing.T) {
	writer := newCorrelatedWriteRecorder()
	fixture := newCorrelatedFixture(t, writer, "bounded")
	stats := fixture.roundTripper.Stats()
	if stats.Capacity <= 0 {
		t.Fatalf("round-trip capacity = %d, want positive", stats.Capacity)
	}

	type pendingCall struct {
		cancel context.CancelFunc
		result <-chan correlatedResult
	}
	pending := make([]pendingCall, 0, stats.Capacity)
	for i := 0; i < stats.Capacity; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		pending = append(pending, pendingCall{
			cancel: cancel,
			result: startCorrelatedRoundTrip(ctx, fixture.roundTripper, fixture.request),
		})
		_ = writer.next(t)
	}
	waitForInFlight(t, fixture.roundTripper, stats.Capacity)

	if _, err := fixture.roundTripper.RoundTrip(context.Background(), fixture.request); !errors.Is(err, api.ErrCorrelatedRoundTripCapacity) {
		t.Fatalf("over-capacity RoundTrip() error = %v, want capacity error", err)
	}
	if got := writer.count(); got != stats.Capacity {
		t.Fatalf("wire writes at capacity = %d, want %d", got, stats.Capacity)
	}

	for _, call := range pending {
		call.cancel()
	}
	for _, call := range pending {
		got := receiveCorrelatedResult(t, call.result)
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("pending RoundTrip() error = %v, want context canceled", got.err)
		}
	}
	waitForInFlight(t, fixture.roundTripper, 0)
	if got := fixture.roundTripper.Stats().Tombstones; got > stats.TombstoneCapacity {
		t.Fatalf("tombstones = %d, capacity = %d", got, stats.TombstoneCapacity)
	}
}

func TestCorrelatedRoundTripCounterExhaustionDoesNotWrap(t *testing.T) {
	writer := newCorrelatedWriteRecorder()
	fixture := newCorrelatedFixture(t, writer, "exhaustion")
	atomic.StoreUint64(&fixture.sender.msgNum, math.MaxUint64)

	if _, err := fixture.roundTripper.RoundTrip(context.Background(), fixture.request); !errors.Is(err, api.ErrCorrelatedCounterExhausted) {
		t.Fatalf("RoundTrip() error = %v, want counter exhaustion", err)
	}
	if got := atomic.LoadUint64(&fixture.sender.msgNum); got != math.MaxUint64 {
		t.Fatalf("message counter after exhaustion = %d, want %d", got, uint64(math.MaxUint64))
	}
	if got := writer.count(); got != 0 {
		t.Fatalf("wire writes after exhaustion = %d, want 0", got)
	}
	if !fixture.roundTripper.Stats().Exhausted {
		t.Fatal("Stats().Exhausted = false, want true")
	}
}

func TestCorrelatedRoundTripSameSKIReplacementRetiresOldGeneration(t *testing.T) {
	local := NewDeviceLocal(
		"brand",
		"model",
		"serial",
		"code",
		"local",
		model.DeviceTypeTypeEnergyManagementSystem,
		model.NetworkManagementFeatureSetTypeSmart,
	)
	oldWriter := newCorrelatedWriteRecorder()
	oldRemote := local.SetupRemoteDevice("replacement", oldWriter).(*DeviceRemote)
	_ = oldWriter.next(t) // Detailed Discovery consumes generation-local key 1.
	oldRemote.address = util.Ptr(model.AddressDeviceType("old-remote"))
	oldRoundTripper := oldRemote.Sender().(api.CorrelatedRoundTripper)

	source := *local.NodeManagement().Address()
	destination := *NodeManagementAddress(oldRemote.Address())
	function := model.FunctionTypeNodeManagementDetailedDiscoveryData
	request := api.CorrelatedRequest{
		Classifier:  model.CmdClassifierTypeRead,
		Source:      source,
		Destination: destination,
		Cmd:         model.CmdType{Function: &function},
	}
	oldResult := startCorrelatedRoundTrip(context.Background(), oldRoundTripper, request)
	oldRequest := oldWriter.next(t)

	newWriter := newCorrelatedWriteRecorder()
	newRemote := local.SetupRemoteDevice("replacement", newWriter).(*DeviceRemote)
	_ = newWriter.next(t) // Detailed Discovery consumes generation-local key 1.
	newRemote.address = util.Ptr(model.AddressDeviceType("new-remote"))
	if newRemote == oldRemote {
		t.Fatal("same-SKI setup reused the prior remote generation")
	}
	if got := receiveCorrelatedResult(t, oldResult); !errors.Is(got.err, api.ErrCorrelatedRoundTripClosed) {
		t.Fatalf("old RoundTrip() error = %v, want sender closed", got.err)
	}
	if !oldRoundTripper.Stats().Closed {
		t.Fatal("old sender Stats().Closed = false, want true")
	}

	newRoundTripper := newRemote.Sender().(api.CorrelatedRoundTripper)
	newRequest := request
	newRequest.Destination = *NodeManagementAddress(newRemote.Address())
	newResult := startCorrelatedRoundTrip(context.Background(), newRoundTripper, newRequest)
	newRequestDatagram := newWriter.next(t)
	if *newRequestDatagram.Header.MsgCounter != *oldRequest.Header.MsgCounter {
		t.Fatalf(
			"test did not exercise cross-generation same wire key: old=%d new=%d",
			*oldRequest.Header.MsgCounter,
			*newRequestDatagram.Header.MsgCounter,
		)
	}

	noError := model.CmdType{
		ResultData: &model.ResultDataType{ErrorNumber: util.Ptr(model.ErrorNumberTypeNoError)},
	}
	_, _ = oldRemote.HandleSpineMesssage(
		correlatedResponse(oldRequest, model.CmdClassifierTypeResult, []model.CmdType{noError}),
	)
	assertNoCorrelatedResult(t, newResult)
	_, _ = newRemote.HandleSpineMesssage(
		correlatedResponse(newRequestDatagram, model.CmdClassifierTypeResult, []model.CmdType{noError}),
	)
	got := receiveCorrelatedResult(t, newResult)
	var protocolErr *api.CorrelatedProtocolError
	if !errors.As(got.err, &protocolErr) {
		t.Fatalf("new generation RoundTrip() error = %T %v, want protocol error", got.err, got.err)
	}
}

func TestCorrelatedRoundTripRetiredReaderHasNoLegacyEffects(t *testing.T) {
	tests := []struct {
		name   string
		retire func(*correlatedFixture)
	}{
		{
			name: "same-SKI replacement",
			retire: func(fixture *correlatedFixture) {
				replacement := NewDeviceRemote(
					fixture.local,
					fixture.remote.Ski(),
					NewSender(newCorrelatedWriteRecorder()),
				)
				fixture.local.AddRemoteDeviceForSki(fixture.remote.Ski(), replacement)
			},
		},
		{
			name: "disconnect",
			retire: func(fixture *correlatedFixture) {
				fixture.local.RemoveRemoteDeviceConnection(fixture.remote.Ski())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := newCorrelatedWriteRecorder()
			fixture := newCorrelatedFixture(t, writer, "retired-reader")

			result := startCorrelatedRoundTrip(context.Background(), fixture.roundTripper, fixture.request)
			request := writer.next(t)
			callback := make(chan api.ResponseMessage, 1)
			if err := fixture.localFeature.AddResponseCallback(
				*request.Header.MsgCounter,
				func(message api.ResponseMessage) {
					callback <- message
				},
			); err != nil {
				t.Fatalf("AddResponseCallback() error = %v", err)
			}

			tt.retire(fixture)
			if got := receiveCorrelatedResult(t, result); !errors.Is(got.err, api.ErrCorrelatedRoundTripClosed) {
				t.Fatalf("retired RoundTrip() error = %v, want sender closed", got.err)
			}

			events := &correlatedEventCounter{}
			if err := Events.subscribe(api.EventHandlerLevelCore, events); err != nil {
				t.Fatalf("Events.subscribe() error = %v", err)
			}
			defer func() {
				_ = Events.unsubscribe(api.EventHandlerLevelCore, events)
			}()

			_, err := fixture.remote.HandleSpineMesssage(
				correlatedResponse(request, model.CmdClassifierTypeReply, []model.CmdType{fixture.reply}),
			)
			if !errors.Is(err, api.ErrCorrelatedRoundTripClosed) {
				t.Errorf("retired HandleSpineMesssage() error = %v, want sender closed", err)
			}
			got, _ := fixture.remoteFeature.DataCopy(
				model.FunctionTypeDeviceClassificationManufacturerData,
			).(*model.DeviceClassificationManufacturerDataType)
			if got != nil {
				t.Errorf("retired reader mutated remote feature cache: %+v", got)
			}
			select {
			case message := <-callback:
				t.Errorf("retired reader invoked legacy callback: %+v", message)
			case <-time.After(25 * time.Millisecond):
			}
			if got := events.count.Load(); got != 0 {
				t.Errorf("retired reader published %d events, want 0", got)
			}
		})
	}
}

func TestCorrelatedRoundTripCloseLinearizesWhileAdmittedSendUnwinds(t *testing.T) {
	writer := newCorrelatedWriteRecorder()
	fixture := newCorrelatedFixture(t, writer, "send-admission")
	writeEntered := make(chan model.DatagramType, 1)
	releaseWrite := make(chan struct{})
	writer.onWrite = func(request model.DatagramType) {
		writeEntered <- request
		<-releaseWrite
	}

	result := startCorrelatedRoundTrip(context.Background(), fixture.roundTripper, fixture.request)
	var admittedRequest model.DatagramType
	select {
	case admittedRequest = <-writeEntered:
	case <-time.After(correlatedTestTimeout):
		t.Fatal("timed out waiting for admitted transport write")
	}

	retirementDone := make(chan struct{})
	go func() {
		fixture.local.RemoveRemoteDeviceConnection(fixture.remote.Ski())
		close(retirementDone)
	}()

	retiredWhileWriteBlocked := false
	select {
	case <-retirementDone:
		retiredWhileWriteBlocked = true
	case <-time.After(25 * time.Millisecond):
	}

	if retiredWhileWriteBlocked {
		if stats := fixture.roundTripper.Stats(); !stats.Closed || stats.InFlight != 0 {
			t.Errorf("Stats() after Close = %+v, want closed with no pending operation", stats)
		}
		if got := fixture.local.RemoteDeviceForSki(fixture.remote.Ski()); got != nil {
			t.Errorf("remote graph entry after retirement = %T, want nil", got)
		}
		if _, err := fixture.roundTripper.RoundTrip(
			context.Background(),
			fixture.request,
		); !errors.Is(err, api.ErrCorrelatedRoundTripClosed) {
			t.Errorf("post-retirement RoundTrip() error = %v, want sender closed", err)
		}
		if got := writer.count(); got != 1 {
			t.Errorf("wire writes after retirement = %d, want only the admitted write", got)
		}
		_, err := fixture.remote.HandleSpineMesssage(
			correlatedResponse(
				admittedRequest,
				model.CmdClassifierTypeReply,
				[]model.CmdType{fixture.reply},
			),
		)
		if !errors.Is(err, api.ErrCorrelatedRoundTripClosed) {
			t.Errorf("post-retirement HandleSpineMesssage() error = %v, want sender closed", err)
		}
	}

	close(releaseWrite)
	if !retiredWhileWriteBlocked {
		select {
		case <-retirementDone:
		case <-time.After(correlatedTestTimeout):
			t.Fatal("retirement did not return after transport write completed")
		}
		t.Error("sender and graph did not logically retire while the admitted transport write unwound")
	}

	if got := receiveCorrelatedResult(t, result); !errors.Is(got.err, api.ErrCorrelatedRoundTripClosed) {
		t.Fatalf("admitted RoundTrip() error = %v, want sender closed", got.err)
	}
}

func TestCorrelatedRoundTripWriterCanCloseOwnSender(t *testing.T) {
	writer := newCorrelatedWriteRecorder()
	fixture := newCorrelatedFixture(t, writer, "self-close")
	callback := make(chan api.ResponseMessage, 1)
	events := &correlatedEventCounter{}
	if err := Events.subscribe(api.EventHandlerLevelCore, events); err != nil {
		t.Fatalf("Events.subscribe() error = %v", err)
	}
	defer func() {
		_ = Events.unsubscribe(api.EventHandlerLevelCore, events)
	}()

	type observation struct {
		addCallbackErr error
		closeErr       error
		postSendErr    error
		postReceiveErr error
		cachedData     *model.DeviceClassificationManufacturerDataType
		stats          api.CorrelatedRoundTripStats
	}
	observed := make(chan observation, 1)
	writer.onWrite = func(request model.DatagramType) {
		value := observation{}
		value.addCallbackErr = fixture.localFeature.AddResponseCallback(
			*request.Header.MsgCounter,
			func(message api.ResponseMessage) {
				callback <- message
			},
		)
		value.closeErr = fixture.roundTripper.Close()
		_, value.postSendErr = fixture.roundTripper.RoundTrip(
			context.Background(),
			fixture.request,
		)
		_, value.postReceiveErr = fixture.remote.HandleSpineMesssage(
			correlatedResponse(request, model.CmdClassifierTypeReply, []model.CmdType{fixture.reply}),
		)
		value.cachedData, _ = fixture.remoteFeature.DataCopy(
			model.FunctionTypeDeviceClassificationManufacturerData,
		).(*model.DeviceClassificationManufacturerDataType)
		value.stats = fixture.roundTripper.Stats()
		observed <- value
	}

	result := startCorrelatedRoundTrip(context.Background(), fixture.roundTripper, fixture.request)
	var got observation
	select {
	case got = <-observed:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("writer calling Close() on its own sender deadlocked")
	}

	if got.addCallbackErr != nil {
		t.Errorf("AddResponseCallback() error = %v", got.addCallbackErr)
	}
	if got.closeErr != nil {
		t.Errorf("reentrant Close() error = %v", got.closeErr)
	}
	if !errors.Is(got.postSendErr, api.ErrCorrelatedRoundTripClosed) {
		t.Errorf("post-close RoundTrip() error = %v, want sender closed", got.postSendErr)
	}
	if !errors.Is(got.postReceiveErr, api.ErrCorrelatedRoundTripClosed) {
		t.Errorf("post-close HandleSpineMesssage() error = %v, want sender closed", got.postReceiveErr)
	}
	if got.cachedData != nil {
		t.Errorf("post-close receive mutated remote feature cache: %+v", got.cachedData)
	}
	if !got.stats.Closed || got.stats.InFlight != 0 {
		t.Errorf("Stats() after reentrant Close = %+v, want closed with no pending operation", got.stats)
	}
	if value := receiveCorrelatedResult(t, result); !errors.Is(value.err, api.ErrCorrelatedRoundTripClosed) {
		t.Errorf("pending RoundTrip() error = %v, want sender closed", value.err)
	}
	if got := writer.count(); got != 1 {
		t.Errorf("wire writes = %d, want only the already-admitted write", got)
	}
	select {
	case message := <-callback:
		t.Errorf("post-close receive invoked legacy callback: %+v", message)
	case <-time.After(25 * time.Millisecond):
	}
	if got := events.count.Load(); got != 0 {
		t.Errorf("post-close receive published %d events, want 0", got)
	}
}

func TestCorrelatedRoundTripReentrantSendAndDeliveryDoNotDeadlock(t *testing.T) {
	writer := newCorrelatedWriteRecorder()
	fixture := newCorrelatedFixture(t, writer, "reentrant")

	var calls atomic.Int32
	nestedDone := make(chan correlatedResult, 1)
	writer.onWrite = func(request model.DatagramType) {
		switch calls.Add(1) {
		case 1:
			response, err := fixture.roundTripper.RoundTrip(context.Background(), fixture.request)
			nestedDone <- correlatedResult{response: response, err: err}
			_, _ = fixture.remote.HandleSpineMesssage(
				correlatedResponse(request, model.CmdClassifierTypeReply, []model.CmdType{fixture.reply}),
			)
		case 2:
			_, _ = fixture.remote.HandleSpineMesssage(
				correlatedResponse(request, model.CmdClassifierTypeReply, []model.CmdType{fixture.reply}),
			)
		default:
			t.Errorf("unexpected write count %d", calls.Load())
		}
	}

	outerResult := startCorrelatedRoundTrip(context.Background(), fixture.roundTripper, fixture.request)
	if got := receiveCorrelatedResult(t, nestedDone); got.err != nil {
		t.Fatalf("nested RoundTrip() error = %v", got.err)
	}
	if got := receiveCorrelatedResult(t, outerResult); got.err != nil {
		t.Fatalf("outer RoundTrip() error = %v", got.err)
	}
	waitForInFlight(t, fixture.roundTripper, 0)
}

func TestCorrelatedRoundTripOutOfOrderRepliesStayCorrelated(t *testing.T) {
	writer := newCorrelatedWriteRecorder()
	fixture := newCorrelatedFixture(t, writer, "ordering")
	firstResult := startCorrelatedRoundTrip(context.Background(), fixture.roundTripper, fixture.request)
	firstRequest := writer.next(t)
	secondResult := startCorrelatedRoundTrip(context.Background(), fixture.roundTripper, fixture.request)
	secondRequest := writer.next(t)

	firstReply := fixture.reply
	firstReply.DeviceClassificationManufacturerData.BrandName = util.Ptr(model.DeviceClassificationStringType("first"))
	secondReply := fixture.reply
	secondReply.DeviceClassificationManufacturerData = &model.DeviceClassificationManufacturerDataType{
		BrandName: util.Ptr(model.DeviceClassificationStringType("second")),
	}

	_, _ = fixture.remote.HandleSpineMesssage(
		correlatedResponse(secondRequest, model.CmdClassifierTypeReply, []model.CmdType{secondReply}),
	)
	_, _ = fixture.remote.HandleSpineMesssage(
		correlatedResponse(firstRequest, model.CmdClassifierTypeReply, []model.CmdType{firstReply}),
	)

	first := receiveCorrelatedResult(t, firstResult)
	second := receiveCorrelatedResult(t, secondResult)
	if first.err != nil || second.err != nil {
		t.Fatalf("out-of-order errors: first=%v second=%v", first.err, second.err)
	}
	if got := *first.response.Cmd.DeviceClassificationManufacturerData.BrandName; got != "first" {
		t.Fatalf("first response brand = %q, want first", got)
	}
	if got := *second.response.Cmd.DeviceClassificationManufacturerData.BrandName; got != "second" {
		t.Fatalf("second response brand = %q, want second", got)
	}
}

func TestCorrelatedRoundTripDoesNotShareLegacyRequestDeduplication(t *testing.T) {
	writer := newCorrelatedWriteRecorder()
	fixture := newCorrelatedFixture(t, writer, "dedup")
	cmds := []model.CmdType{fixture.request.Cmd}

	legacyKey, err := fixture.sender.Request(
		fixture.request.Classifier,
		&fixture.request.Source,
		&fixture.request.Destination,
		fixture.request.AckRequest,
		cmds,
	)
	if err != nil {
		t.Fatalf("legacy Request() error = %v", err)
	}
	legacyRequest := writer.next(t)
	duplicateKey, err := fixture.sender.Request(
		fixture.request.Classifier,
		&fixture.request.Source,
		&fixture.request.Destination,
		fixture.request.AckRequest,
		cmds,
	)
	if err != nil {
		t.Fatalf("duplicate legacy Request() error = %v", err)
	}
	if *duplicateKey != *legacyKey {
		t.Fatalf("legacy duplicate key = %d, want %d", *duplicateKey, *legacyKey)
	}

	result := startCorrelatedRoundTrip(context.Background(), fixture.roundTripper, fixture.request)
	correlatedRequest := writer.next(t)
	if *correlatedRequest.Header.MsgCounter == *legacyRequest.Header.MsgCounter {
		t.Fatalf("correlated request shared legacy key %d", *legacyRequest.Header.MsgCounter)
	}
	if got := writer.count(); got != 2 {
		t.Fatalf("wire writes = %d, want one legacy plus one correlated", got)
	}
	_, _ = fixture.remote.HandleSpineMesssage(
		correlatedResponse(correlatedRequest, model.CmdClassifierTypeReply, []model.CmdType{fixture.reply}),
	)
	if got := receiveCorrelatedResult(t, result); got.err != nil {
		t.Fatalf("correlated RoundTrip() error = %v", got.err)
	}
}
