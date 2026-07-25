package spine

import (
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-spine-go/api"
)

func TestIssue3PublishUsesCapturedSnapshotDuringUnsubscribe(t *testing.T) {
	core := &issue3BlockingHandler{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	application := &issue3SignalHandler{called: make(chan struct{}, 1)}
	if err := Events.subscribe(api.EventHandlerLevelCore, core); err != nil {
		t.Fatal(err)
	}
	if err := Events.Subscribe(application); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = Events.unsubscribe(api.EventHandlerLevelCore, core)
		_ = Events.Unsubscribe(application)
	})

	published := make(chan struct{})
	go func() {
		Events.Publish(api.EventPayload{})
		close(published)
	}()
	<-core.entered

	if err := Events.Unsubscribe(application); err != nil {
		t.Fatal(err)
	}
	close(core.release)
	select {
	case <-published:
	case <-time.After(time.Second):
		t.Fatal("publish did not complete")
	}
	select {
	case <-application.called:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("in-flight publish did not use its captured subscriber snapshot")
	}
}

type issue3BlockingHandler struct {
	entered chan struct{}
	release chan struct{}
}

func (handler *issue3BlockingHandler) HandleEvent(api.EventPayload) {
	close(handler.entered)
	<-handler.release
}

type issue3SignalHandler struct {
	called chan struct{}
}

func (handler *issue3SignalHandler) HandleEvent(api.EventPayload) {
	handler.called <- struct{}{}
}
