package spine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	shiplogging "github.com/Project-Helianthus/helianthus-ship-go/logging"
	"github.com/Project-Helianthus/helianthus-spine-go/api"
	"github.com/Project-Helianthus/helianthus-spine-go/model"
	"github.com/Project-Helianthus/helianthus-spine-go/util"
)

type issue13TerminalObservation struct {
	err    error
	writes int
}

type issue13ControlledContext struct {
	done chan struct{}
	once sync.Once
	mu   sync.RWMutex
	err  error
}

type issue13BlockingLogger struct {
	shiplogging.NoLogging
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
	releaseOnce sync.Once
}

func newIssue13ControlledContext() *issue13ControlledContext {
	return &issue13ControlledContext{done: make(chan struct{})}
}

func newIssue13BlockingLogger() *issue13BlockingLogger {
	return &issue13BlockingLogger{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (l *issue13BlockingLogger) Debug(...interface{}) {
	l.enteredOnce.Do(func() {
		close(l.entered)
	})
	<-l.release
}

func (l *issue13BlockingLogger) unblock() {
	l.releaseOnce.Do(func() {
		close(l.release)
	})
}

func (*issue13ControlledContext) Deadline() (deadline time.Time, ok bool) {
	return time.Time{}, false
}

func (c *issue13ControlledContext) Done() <-chan struct{} {
	return c.done
}

func (c *issue13ControlledContext) Err() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.err
}

func (*issue13ControlledContext) Value(any) any {
	return nil
}

func (c *issue13ControlledContext) finish(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.done)
	})
}

func issue13WaitForSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(correlatedTestTimeout):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func issue13WaitForGroup(t *testing.T, group *sync.WaitGroup, label string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		group.Wait()
		close(done)
	}()
	issue13WaitForSignal(t, done, label)
}

func issue13RequireDisposition(
	t *testing.T,
	err error,
	want api.DispatchDisposition,
) *api.CorrelatedRoundTripError {
	t.Helper()

	if err == nil {
		t.Fatal("RoundTrip() error = nil, want typed terminal error")
	}
	var got *api.CorrelatedRoundTripError
	if !errors.As(err, &got) {
		t.Fatalf("RoundTrip() error = %T %v, want CorrelatedRoundTripError", err, err)
	}
	if got.Cause == nil {
		t.Fatal("CorrelatedRoundTripError.Cause = nil")
	}
	if got.Disposition != want {
		t.Fatalf(
			"CorrelatedRoundTripError.Disposition = %v, want %v",
			got.Disposition,
			want,
		)
	}
	if unwrapped := errors.Unwrap(got); unwrapped != got.Cause {
		t.Fatalf("errors.Unwrap() = %T %v, want exact Cause", unwrapped, unwrapped)
	}
	if !errors.Is(err, got.Cause) {
		t.Fatalf("errors.Is(error, CorrelatedRoundTripError.Cause) = false: %v", err)
	}
	return got
}

func issue13Fixture(
	t *testing.T,
	name string,
) (*correlatedWriteRecorder, *correlatedFixture) {
	t.Helper()
	writer := newCorrelatedWriteRecorder()
	return writer, newCorrelatedFixture(t, writer, "issue13-"+name)
}

func TestIssue13SameClosedCauseSeparatesTransportHandoff(t *testing.T) {
	tests := []struct {
		name            string
		wantDisposition api.DispatchDisposition
		wantWrites      int
		run             func(*testing.T) issue13TerminalObservation
	}{
		{
			name:            "closed before writer",
			wantDisposition: api.NoTransportHandoff,
			wantWrites:      0,
			run: func(t *testing.T) issue13TerminalObservation {
				writer, fixture := issue13Fixture(t, "closed-pre-writer")
				if err := fixture.roundTripper.Close(); err != nil {
					t.Fatalf("Close() error = %v", err)
				}
				_, err := fixture.roundTripper.RoundTrip(context.Background(), fixture.request)
				return issue13TerminalObservation{err: err, writes: writer.count()}
			},
		},
		{
			name:            "closed from writer callback",
			wantDisposition: api.TransportHandoffPossible,
			wantWrites:      1,
			run: func(t *testing.T) issue13TerminalObservation {
				writer, fixture := issue13Fixture(t, "closed-post-writer")
				writer.onWrite = func(model.DatagramType) {
					if err := fixture.roundTripper.Close(); err != nil {
						t.Errorf("Close() from writer error = %v", err)
					}
				}
				result := startCorrelatedRoundTrip(
					context.Background(),
					fixture.roundTripper,
					fixture.request,
				)
				got := receiveCorrelatedResult(t, result)
				return issue13TerminalObservation{err: got.err, writes: writer.count()}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.run(t)
			if got.writes != tt.wantWrites {
				t.Fatalf("SHIP writer count = %d, want %d", got.writes, tt.wantWrites)
			}
			issue13RequireDisposition(t, got.err, tt.wantDisposition)
			if !errors.Is(got.err, api.ErrCorrelatedRoundTripClosed) {
				t.Fatalf(
					"RoundTrip() error = %v, want ErrCorrelatedRoundTripClosed identity",
					got.err,
				)
			}
		})
	}
}

func TestIssue13CloseAfterRegistrationBeforeWriterHasNoHandoff(t *testing.T) {
	writer, fixture := issue13Fixture(t, "registered-before-writer")
	logger := newIssue13BlockingLogger()
	previousLogger := shiplogging.Log()
	shiplogging.SetLogging(logger)
	defer func() {
		logger.unblock()
		shiplogging.SetLogging(previousLogger)
	}()

	result := startCorrelatedRoundTrip(
		context.Background(),
		fixture.roundTripper,
		fixture.request,
	)
	issue13WaitForSignal(t, logger.entered, "registered call to reach pre-writer barrier")
	if got := fixture.roundTripper.Stats().InFlight; got != 1 {
		t.Fatalf("in-flight calls at pre-writer barrier = %d, want 1", got)
	}

	if err := fixture.roundTripper.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got := writer.count(); got != 0 {
		t.Fatalf("SHIP writer count while pre-writer call is blocked = %d, want 0", got)
	}

	logger.unblock()
	got := receiveCorrelatedResult(t, result)
	if gotWrites := writer.count(); gotWrites != 0 {
		t.Fatalf("final SHIP writer count = %d, want 0", gotWrites)
	}
	issue13RequireDisposition(t, got.err, api.NoTransportHandoff)
	if !errors.Is(got.err, api.ErrCorrelatedRoundTripClosed) {
		t.Fatalf("RoundTrip() error = %v, want ErrCorrelatedRoundTripClosed", got.err)
	}
}

func TestIssue13SameSenderRetainsPerCallMixedHandoffPhases(t *testing.T) {
	writer, fixture := issue13Fixture(t, "same-sender-mixed-phases")

	postWriterResult := startCorrelatedRoundTrip(
		context.Background(),
		fixture.roundTripper,
		fixture.request,
	)
	_ = writer.next(t)

	logger := newIssue13BlockingLogger()
	previousLogger := shiplogging.Log()
	shiplogging.SetLogging(logger)
	defer func() {
		logger.unblock()
		shiplogging.SetLogging(previousLogger)
	}()

	preWriterResult := startCorrelatedRoundTrip(
		context.Background(),
		fixture.roundTripper,
		fixture.request,
	)
	issue13WaitForSignal(t, logger.entered, "second call to reach pre-writer barrier")
	if got := fixture.roundTripper.Stats().InFlight; got != 2 {
		t.Fatalf("in-flight calls at mixed-phase barrier = %d, want 2", got)
	}

	if err := fixture.roundTripper.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got := writer.count(); got != 1 {
		t.Errorf("SHIP writer count before releasing second call = %d, want 1", got)
	}

	logger.unblock()
	postWriter := receiveCorrelatedResult(t, postWriterResult)
	preWriter := receiveCorrelatedResult(t, preWriterResult)

	if got := writer.count(); got != 1 {
		t.Fatalf("final SHIP writer count = %d, want first call only", got)
	}
	issue13RequireDisposition(t, postWriter.err, api.TransportHandoffPossible)
	issue13RequireDisposition(t, preWriter.err, api.NoTransportHandoff)
	if !errors.Is(postWriter.err, api.ErrCorrelatedRoundTripClosed) ||
		!errors.Is(preWriter.err, api.ErrCorrelatedRoundTripClosed) {
		t.Fatalf(
			"mixed-phase errors = (%v, %v), want shared ErrCorrelatedRoundTripClosed identity",
			postWriter.err,
			preWriter.err,
		)
	}
}

func TestIssue13PreWriterTerminalTaxonomy(t *testing.T) {
	tests := []struct {
		name        string
		wantCause   error
		wantMessage string
		assertCause func(*testing.T, error)
		run         func(*testing.T) issue13TerminalObservation
	}{
		{
			name:      "pre-cancel",
			wantCause: context.Canceled,
			run: func(t *testing.T) issue13TerminalObservation {
				writer, fixture := issue13Fixture(t, "pre-cancel")
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				_, err := fixture.roundTripper.RoundTrip(ctx, fixture.request)
				return issue13TerminalObservation{err: err, writes: writer.count()}
			},
		},
		{
			name:        "nil context",
			wantMessage: "correlated round-trip context is nil",
			run: func(t *testing.T) issue13TerminalObservation {
				writer, fixture := issue13Fixture(t, "nil-context")
				_, err := fixture.roundTripper.RoundTrip(nil, fixture.request)
				return issue13TerminalObservation{err: err, writes: writer.count()}
			},
		},
		{
			name:        "unsupported classifier",
			wantMessage: "unsupported correlated request classifier",
			run: func(t *testing.T) issue13TerminalObservation {
				writer, fixture := issue13Fixture(t, "unsupported-classifier")
				request := fixture.request
				request.Classifier = model.CmdClassifierTypeNotify
				_, err := fixture.roundTripper.RoundTrip(context.Background(), request)
				return issue13TerminalObservation{err: err, writes: writer.count()}
			},
		},
		{
			name:        "missing source",
			wantMessage: "correlated request source device address is required",
			run: func(t *testing.T) issue13TerminalObservation {
				writer, fixture := issue13Fixture(t, "missing-source")
				request := fixture.request
				request.Source = model.FeatureAddressType{}
				_, err := fixture.roundTripper.RoundTrip(context.Background(), request)
				return issue13TerminalObservation{err: err, writes: writer.count()}
			},
		},
		{
			name:        "missing destination",
			wantMessage: "correlated request destination device address is required",
			run: func(t *testing.T) issue13TerminalObservation {
				writer, fixture := issue13Fixture(t, "missing-destination")
				request := fixture.request
				request.Destination = model.FeatureAddressType{}
				_, err := fixture.roundTripper.RoundTrip(context.Background(), request)
				return issue13TerminalObservation{err: err, writes: writer.count()}
			},
		},
		{
			name: "invalid request",
			assertCause: func(t *testing.T, err error) {
				t.Helper()
				var protocolErr *api.CorrelatedProtocolError
				if !errors.As(err, &protocolErr) {
					t.Fatalf("error = %T %v, want CorrelatedProtocolError cause", err, err)
				}
			},
			run: func(t *testing.T) issue13TerminalObservation {
				writer, fixture := issue13Fixture(t, "invalid-request")
				request := fixture.request
				request.Cmd = model.CmdType{}
				_, err := fixture.roundTripper.RoundTrip(context.Background(), request)
				return issue13TerminalObservation{err: err, writes: writer.count()}
			},
		},
		{
			name:      "admission capacity",
			wantCause: api.ErrCorrelatedRoundTripCapacity,
			run: func(t *testing.T) issue13TerminalObservation {
				writer, fixture := issue13Fixture(t, "capacity")
				fixture.sender.roundTripMux.Lock()
				for index := 0; index < maxPendingCorrelatedRoundTrips; index++ {
					key := model.MsgCounterType(index + 100)
					fixture.sender.pendingRoundTrips[key] = &pendingCorrelatedRoundTrip{}
				}
				fixture.sender.roundTripMux.Unlock()

				_, err := fixture.roundTripper.RoundTrip(context.Background(), fixture.request)
				return issue13TerminalObservation{err: err, writes: writer.count()}
			},
		},
		{
			name:      "admission counter exhaustion",
			wantCause: api.ErrCorrelatedCounterExhausted,
			run: func(t *testing.T) issue13TerminalObservation {
				writer, fixture := issue13Fixture(t, "counter-exhaustion")
				atomic.StoreUint64(&fixture.sender.msgNum, math.MaxUint64)
				_, err := fixture.roundTripper.RoundTrip(context.Background(), fixture.request)
				return issue13TerminalObservation{err: err, writes: writer.count()}
			},
		},
		{
			name:      "admission key in flight",
			wantCause: api.ErrCorrelatedKeyInFlight,
			run: func(t *testing.T) issue13TerminalObservation {
				writer, fixture := issue13Fixture(t, "key-in-flight")
				fixture.sender.pendingRoundTrips[1] = &pendingCorrelatedRoundTrip{}
				_, err := fixture.roundTripper.RoundTrip(context.Background(), fixture.request)
				return issue13TerminalObservation{err: err, writes: writer.count()}
			},
		},
		{
			name:      "admission key retired",
			wantCause: api.ErrCorrelatedKeyRetired,
			run: func(t *testing.T) issue13TerminalObservation {
				writer, fixture := issue13Fixture(t, "key-retired")
				fixture.sender.retiredHighWatermark = 1
				_, err := fixture.roundTripper.RoundTrip(context.Background(), fixture.request)
				return issue13TerminalObservation{err: err, writes: writer.count()}
			},
		},
		{
			name:        "missing writer",
			wantMessage: "outgoing interface implementation not set",
			run: func(t *testing.T) issue13TerminalObservation {
				writer, fixture := issue13Fixture(t, "missing-writer")
				fixture.sender.writeHandler = nil
				_, err := fixture.roundTripper.RoundTrip(context.Background(), fixture.request)
				return issue13TerminalObservation{err: err, writes: writer.count()}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.run(t)
			typed := issue13RequireDisposition(t, got.err, api.NoTransportHandoff)
			if got.writes != 0 {
				t.Fatalf("SHIP writer count = %d, want 0", got.writes)
			}
			if tt.wantCause != nil && !errors.Is(got.err, tt.wantCause) {
				t.Fatalf("error = %v, want cause identity %v", got.err, tt.wantCause)
			}
			if tt.wantMessage != "" && !strings.Contains(typed.Cause.Error(), tt.wantMessage) {
				t.Fatalf("Cause = %q, want text %q", typed.Cause, tt.wantMessage)
			}
			if tt.assertCause != nil {
				tt.assertCause(t, got.err)
			}
		})
	}
}

func TestIssue13PostWriterTerminalTaxonomy(t *testing.T) {
	tests := []struct {
		name        string
		wantCause   error
		assertCause func(*testing.T, error)
		run         func(*testing.T) issue13TerminalObservation
	}{
		{
			name:      "cancel",
			wantCause: context.Canceled,
			run: func(t *testing.T) issue13TerminalObservation {
				writer, fixture := issue13Fixture(t, "post-cancel")
				ctx := newIssue13ControlledContext()
				result := startCorrelatedRoundTrip(ctx, fixture.roundTripper, fixture.request)
				_ = writer.next(t)
				ctx.finish(context.Canceled)
				got := receiveCorrelatedResult(t, result)
				return issue13TerminalObservation{err: got.err, writes: writer.count()}
			},
		},
		{
			name:      "timeout",
			wantCause: context.DeadlineExceeded,
			run: func(t *testing.T) issue13TerminalObservation {
				writer, fixture := issue13Fixture(t, "post-timeout")
				ctx := newIssue13ControlledContext()
				result := startCorrelatedRoundTrip(ctx, fixture.roundTripper, fixture.request)
				_ = writer.next(t)
				ctx.finish(context.DeadlineExceeded)
				got := receiveCorrelatedResult(t, result)
				return issue13TerminalObservation{err: got.err, writes: writer.count()}
			},
		},
		{
			name:      "close",
			wantCause: api.ErrCorrelatedRoundTripClosed,
			run: func(t *testing.T) issue13TerminalObservation {
				writer, fixture := issue13Fixture(t, "post-close")
				result := startCorrelatedRoundTrip(
					context.Background(),
					fixture.roundTripper,
					fixture.request,
				)
				_ = writer.next(t)
				if err := fixture.roundTripper.Close(); err != nil {
					t.Fatalf("Close() error = %v", err)
				}
				got := receiveCorrelatedResult(t, result)
				return issue13TerminalObservation{err: got.err, writes: writer.count()}
			},
		},
		{
			name:      "disconnect",
			wantCause: api.ErrCorrelatedRoundTripClosed,
			run: func(t *testing.T) issue13TerminalObservation {
				writer, fixture := issue13Fixture(t, "post-disconnect")
				result := startCorrelatedRoundTrip(
					context.Background(),
					fixture.roundTripper,
					fixture.request,
				)
				_ = writer.next(t)
				fixture.local.RemoveRemoteDeviceConnection(fixture.remote.Ski())
				got := receiveCorrelatedResult(t, result)
				return issue13TerminalObservation{err: got.err, writes: writer.count()}
			},
		},
		{
			name: "remote rejection",
			assertCause: func(t *testing.T, err error) {
				t.Helper()
				var remoteErr *api.CorrelatedRemoteError
				if !errors.As(err, &remoteErr) {
					t.Fatalf("error = %T %v, want CorrelatedRemoteError cause", err, err)
				}
				if remoteErr.ErrorNumber != model.ErrorNumberTypeCommandRejected {
					t.Fatalf(
						"remote error number = %d, want %d",
						remoteErr.ErrorNumber,
						model.ErrorNumberTypeCommandRejected,
					)
				}
			},
			run: func(t *testing.T) issue13TerminalObservation {
				writer, fixture := issue13Fixture(t, "remote-rejection")
				result := startCorrelatedRoundTrip(
					context.Background(),
					fixture.roundTripper,
					fixture.request,
				)
				request := writer.next(t)
				resultCmd := model.CmdType{
					ResultData: &model.ResultDataType{
						ErrorNumber: util.Ptr(model.ErrorNumberTypeCommandRejected),
						Description: util.Ptr(model.DescriptionType("rejected")),
					},
				}
				_, _ = fixture.remote.HandleSpineMesssage(
					correlatedResponse(
						request,
						model.CmdClassifierTypeResult,
						[]model.CmdType{resultCmd},
					),
				)
				got := receiveCorrelatedResult(t, result)
				return issue13TerminalObservation{err: got.err, writes: writer.count()}
			},
		},
		{
			name: "malformed response",
			assertCause: func(t *testing.T, err error) {
				t.Helper()
				var protocolErr *api.CorrelatedProtocolError
				if !errors.As(err, &protocolErr) {
					t.Fatalf("error = %T %v, want CorrelatedProtocolError cause", err, err)
				}
				if protocolErr.Message != "decode correlated response JSON" {
					t.Fatalf(
						"CorrelatedProtocolError.Message = %q, want malformed decode path",
						protocolErr.Message,
					)
				}
				if protocolErr.Cause == nil {
					t.Fatal("CorrelatedProtocolError.Cause = nil, want malformed JSON cause")
				}
			},
			run: func(t *testing.T) issue13TerminalObservation {
				writer, fixture := issue13Fixture(t, "malformed-response")
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
				if _, err := fixture.remote.HandleSpineMesssage(message); err == nil {
					t.Fatal("HandleSpineMesssage() error = nil, want trailing JSON rejection")
				}
				got := receiveCorrelatedResult(t, result)
				return issue13TerminalObservation{err: got.err, writes: writer.count()}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.run(t)
			issue13RequireDisposition(t, got.err, api.TransportHandoffPossible)
			if got.writes != 1 {
				t.Fatalf("SHIP writer count = %d, want 1", got.writes)
			}
			if tt.wantCause != nil && !errors.Is(got.err, tt.wantCause) {
				t.Fatalf("error = %v, want cause identity %v", got.err, tt.wantCause)
			}
			if tt.assertCause != nil {
				tt.assertCause(t, got.err)
			}
		})
	}
}

func TestIssue13CloseCancelReplyRaceKeepsPostWriterDisposition(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		writer, fixture := issue13Fixture(t, fmt.Sprintf("terminal-race-%d", iteration))
		ctx, cancel := context.WithCancel(context.Background())
		result := startCorrelatedRoundTrip(ctx, fixture.roundTripper, fixture.request)
		request := writer.next(t)

		start := make(chan struct{})
		var racers sync.WaitGroup
		racers.Add(3)
		go func() {
			defer racers.Done()
			<-start
			cancel()
		}()
		go func() {
			defer racers.Done()
			<-start
			_ = fixture.roundTripper.Close()
		}()
		go func() {
			defer racers.Done()
			<-start
			_, _ = fixture.remote.HandleSpineMesssage(
				correlatedResponse(
					request,
					model.CmdClassifierTypeReply,
					[]model.CmdType{fixture.reply},
				),
			)
		}()
		close(start)
		issue13WaitForGroup(t, &racers, "close/cancel/reply racers")

		got := receiveCorrelatedResult(t, result)
		if got.err != nil {
			issue13RequireDisposition(t, got.err, api.TransportHandoffPossible)
			if !errors.Is(got.err, context.Canceled) &&
				!errors.Is(got.err, api.ErrCorrelatedRoundTripClosed) {
				t.Fatalf(
					"iteration %d: RoundTrip() error = %T %v, want success, cancel, or close",
					iteration,
					got.err,
					got.err,
				)
			}
		}
		if gotWrites := writer.count(); gotWrites != 1 {
			t.Fatalf("iteration %d: SHIP writer count = %d, want 1", iteration, gotWrites)
		}
		waitForInFlight(t, fixture.roundTripper, 0)
	}
}
