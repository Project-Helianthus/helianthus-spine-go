package spine

import (
	"sync"

	"github.com/Project-Helianthus/helianthus-spine-go/api"
)

var Events events

type eventHandlerItem struct {
	Level   api.EventHandlerLevel
	Handler api.EventHandlerInterface

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
			return nil
		}
	}

	newHandlerItem := &eventHandlerItem{
		Level:   level,
		Handler: handler,
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

	var newHandlers []*eventHandlerItem
	for _, item := range r.handlers {
		if item.Level != level || item.Handler != handler {
			newHandlers = append(newHandlers, item)
		}
	}

	r.handlers = newHandlers

	return nil
}

// Unsubscribe from getting events
func (r *events) Unsubscribe(handler api.EventHandlerInterface) error {
	return r.unsubscribe(api.EventHandlerLevelApplication, handler)
}

// Publish an event to all subscribers
func (r *events) Publish(payload api.EventPayload) {
	r.mu.Lock()
	handler := make([]*eventHandlerItem, len(r.handlers))
	copy(handler, r.handlers)
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
			return
		}
		payload := item.pending[0]
		item.pending[0] = api.EventPayload{}
		item.pending = item.pending[1:]
		item.dispatchMu.Unlock()

		item.Handler.HandleEvent(payload)
	}
}
