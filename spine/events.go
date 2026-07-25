package spine

import (
	"sync"

	"github.com/Project-Helianthus/helianthus-spine-go/api"
)

var Events events

type eventHandlerItem struct {
	Level   api.EventHandlerLevel
	Handler api.EventHandlerInterface

	owner      *events
	subscribed bool
	captures   int
	dispatchMu sync.Mutex
	pending    []api.EventPayload
	running    bool
}

type events struct {
	mu       sync.Mutex
	muHandle sync.Mutex

	handlers []*eventHandlerItem // event handling outside of the core stack
}

// will be used in EEBUS core directly to access the level EventHandlerLevelCore
func (r *events) subscribe(level api.EventHandlerLevel, handler api.EventHandlerInterface) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, item := range r.handlers {
		if item.Level == level && item.Handler == handler {
			item.subscribed = true
			return nil
		}
	}

	newHandlerItem := &eventHandlerItem{
		Level:      level,
		Handler:    handler,
		owner:      r,
		subscribed: true,
	}
	r.handlers = append(r.handlers, newHandlerItem)

	return nil
}

// Subscribe to message events and handle them in
// the Eventhandler interface implementation
//
// returns an error if EventHandlerLevelCore is used as
// that is only allowed for internal use
func (r *events) Subscribe(handler api.EventHandlerInterface) error {
	return r.subscribe(api.EventHandlerLevelApplication, handler)
}

// will be used in EEBUS core directly to access the level EventHandlerLevelCore
func (r *events) unsubscribe(level api.EventHandlerLevel, handler api.EventHandlerInterface) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, item := range r.handlers {
		if item.Level == level && item.Handler == handler {
			item.subscribed = false
			r.retireLocked(item)
			break
		}
	}

	return nil
}

// Unsubscribe from getting events
func (r *events) Unsubscribe(handler api.EventHandlerInterface) error {
	return r.unsubscribe(api.EventHandlerLevelApplication, handler)
}

// Publish an event to all subscribers
func (r *events) Publish(payload api.EventPayload) {
	r.mu.Lock()
	handler := make([]*eventHandlerItem, 0, len(r.handlers))
	for _, item := range r.handlers {
		if item.subscribed {
			item.captures++
			handler = append(handler, item)
		}
	}
	r.mu.Unlock()

	// Use different locks, so unpublish is possible in the event handlers
	r.muHandle.Lock()
	// process subscribers by level
	handlerLevels := []api.EventHandlerLevel{
		api.EventHandlerLevelCore,
		api.EventHandlerLevelApplication,
	}

	for _, level := range handlerLevels {
		for _, item := range handler {
			if item.Level != level {
				continue
			}

			if level == api.EventHandlerLevelCore {
				// do not run this asynchronously, to make sure all required
				// and expected actions are taken
				item.Handler.HandleEvent(payload)
			} else {
				item.dispatch(payload)
			}
			r.releaseCapture(item)
		}
	}
	r.muHandle.Unlock()
}

func (item *eventHandlerItem) dispatch(payload api.EventPayload) {
	item.dispatchMu.Lock()
	item.pending = append(item.pending, payload)
	if item.running {
		item.dispatchMu.Unlock()
		return
	}
	item.running = true
	item.dispatchMu.Unlock()

	go item.run()
}

func (item *eventHandlerItem) run() {
	for {
		item.dispatchMu.Lock()
		if len(item.pending) == 0 {
			item.running = false
			item.dispatchMu.Unlock()
			item.owner.retire(item)
			return
		}
		payload := item.pending[0]
		item.pending[0] = api.EventPayload{}
		item.pending = item.pending[1:]
		item.dispatchMu.Unlock()

		item.Handler.HandleEvent(payload)
	}
}

func (r *events) releaseCapture(item *eventHandlerItem) {
	r.mu.Lock()
	item.captures--
	r.retireLocked(item)
	r.mu.Unlock()
}

func (r *events) retire(item *eventHandlerItem) {
	r.mu.Lock()
	r.retireLocked(item)
	r.mu.Unlock()
}

func (r *events) retireLocked(item *eventHandlerItem) {
	if item.subscribed || item.captures != 0 {
		return
	}
	item.dispatchMu.Lock()
	idle := !item.running && len(item.pending) == 0
	item.dispatchMu.Unlock()
	if !idle {
		return
	}
	for index, candidate := range r.handlers {
		if candidate == item {
			copy(r.handlers[index:], r.handlers[index+1:])
			r.handlers[len(r.handlers)-1] = nil
			r.handlers = r.handlers[:len(r.handlers)-1]
			return
		}
	}
}
