package spine

import (
	"sync"
	"testing"
	"time"

	"github.com/Project-Helianthus/helianthus-spine-go/api"
)

func TestIssue7ApplicationCallbacksPreservePublicationOrder(t *testing.T) {
	handler := &issue7OrderedHandler{
		firstEntered:  make(chan struct{}),
		releaseFirst:  make(chan struct{}),
		secondEntered: make(chan struct{}),
	}
	if err := Events.Subscribe(handler); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = Events.Unsubscribe(handler)
	})

	Events.Publish(api.EventPayload{Ski: "first"})
	select {
	case <-handler.firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first application callback did not start")
	}

	Events.Publish(api.EventPayload{Ski: "second"})
	select {
	case <-handler.secondEntered:
		t.Fatal("second application callback overtook the blocked first callback")
	case <-time.After(50 * time.Millisecond):
	}

	close(handler.releaseFirst)
	select {
	case <-handler.secondEntered:
	case <-time.After(time.Second):
		t.Fatal("second application callback did not run after the first completed")
	}
	if got := handler.orderSnapshot(); len(got) != 2 || got[0] != "first" || got[1] != "second" {
		t.Fatalf("application callback order = %v, want [first second]", got)
	}
}

type issue7OrderedHandler struct {
	mu            sync.Mutex
	order         []string
	firstEntered  chan struct{}
	releaseFirst  chan struct{}
	secondEntered chan struct{}
}

func (handler *issue7OrderedHandler) HandleEvent(payload api.EventPayload) {
	switch payload.Ski {
	case "first":
		close(handler.firstEntered)
		<-handler.releaseFirst
	case "second":
		close(handler.secondEntered)
	}
	handler.mu.Lock()
	handler.order = append(handler.order, payload.Ski)
	handler.mu.Unlock()
}

func (handler *issue7OrderedHandler) orderSnapshot() []string {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	return append([]string(nil), handler.order...)
}
