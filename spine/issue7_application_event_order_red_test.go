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

func TestIssue7ApplicationSubscribersDispatchIndependently(t *testing.T) {
	slowEntered := make(chan struct{})
	releaseSlow := make(chan struct{})
	fastEntered := make(chan struct{})
	slow := &issue7FuncHandler{handle: func(api.EventPayload) {
		close(slowEntered)
		<-releaseSlow
	}}
	fast := &issue7FuncHandler{handle: func(api.EventPayload) {
		close(fastEntered)
	}}
	if err := Events.Subscribe(slow); err != nil {
		t.Fatal(err)
	}
	if err := Events.Subscribe(fast); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = Events.Unsubscribe(slow)
		_ = Events.Unsubscribe(fast)
	})

	Events.Publish(api.EventPayload{})
	select {
	case <-slowEntered:
	case <-time.After(time.Second):
		t.Fatal("slow subscriber did not start")
	}
	select {
	case <-fastEntered:
	case <-time.After(time.Second):
		t.Fatal("slow subscriber blocked an independent subscriber")
	}
	close(releaseSlow)
}

func TestIssue7ApplicationCallbackMayUnsubscribeItself(t *testing.T) {
	called := make(chan struct{}, 2)
	handler := &issue7FuncHandler{}
	handler.handle = func(api.EventPayload) {
		called <- struct{}{}
		if err := Events.Unsubscribe(handler); err != nil {
			t.Errorf("reentrant unsubscribe: %v", err)
		}
	}
	if err := Events.Subscribe(handler); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = Events.Unsubscribe(handler)
	})

	Events.Publish(api.EventPayload{})
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("reentrant subscriber did not run")
	}
	Events.Publish(api.EventPayload{})
	select {
	case <-called:
		t.Fatal("unsubscribed handler received a later publication")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestIssue7ReSubscribeKeepsTheExistingDispatcher(t *testing.T) {
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	resubscribed := make(chan struct{})
	secondEntered := make(chan struct{})
	handler := &issue7ReSubscribeHandler{
		firstEntered:  firstEntered,
		releaseFirst:  releaseFirst,
		resubscribed:  resubscribed,
		secondEntered: secondEntered,
	}
	if err := Events.Subscribe(handler); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = Events.Unsubscribe(handler)
	})

	Events.Publish(api.EventPayload{Ski: "first"})
	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first callback did not start")
	}
	select {
	case <-resubscribed:
	case <-time.After(time.Second):
		t.Fatal("first callback did not re-subscribe")
	}

	Events.Publish(api.EventPayload{Ski: "second"})
	select {
	case <-secondEntered:
		t.Fatal("re-subscribed callback bypassed the existing dispatcher")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("second callback did not run after the first completed")
	}
}

func TestIssue7UnsubscribedDispatcherRetiresAfterDrain(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	handler := &issue7FuncHandler{handle: func(api.EventPayload) {
		close(entered)
		<-release
	}}
	if err := Events.Subscribe(handler); err != nil {
		t.Fatal(err)
	}

	Events.Publish(api.EventPayload{})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("application callback did not start")
	}
	if err := Events.Unsubscribe(handler); err != nil {
		t.Fatal(err)
	}
	if !issue7HandlerRegistered(handler) {
		t.Fatal("active dispatcher retired before its captured callback drained")
	}
	close(release)

	deadline := time.Now().Add(time.Second)
	for issue7HandlerRegistered(handler) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if issue7HandlerRegistered(handler) {
		t.Fatal("unsubscribed dispatcher remained registered after its queue drained")
	}
}

func issue7HandlerRegistered(handler api.EventHandlerInterface) bool {
	Events.mu.Lock()
	defer Events.mu.Unlock()
	for _, item := range Events.handlers {
		if item.Handler == handler {
			return true
		}
	}
	return false
}

type issue7FuncHandler struct {
	handle func(api.EventPayload)
}

func (handler *issue7FuncHandler) HandleEvent(payload api.EventPayload) {
	handler.handle(payload)
}

type issue7ReSubscribeHandler struct {
	firstEntered  chan struct{}
	releaseFirst  chan struct{}
	resubscribed  chan struct{}
	secondEntered chan struct{}
}

func (handler *issue7ReSubscribeHandler) HandleEvent(payload api.EventPayload) {
	switch payload.Ski {
	case "first":
		close(handler.firstEntered)
		if err := Events.Unsubscribe(handler); err != nil {
			panic(err)
		}
		if err := Events.Subscribe(handler); err != nil {
			panic(err)
		}
		close(handler.resubscribed)
		<-handler.releaseFirst
	case "second":
		close(handler.secondEntered)
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
