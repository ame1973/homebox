package main

import (
	"net/http"
	"sync"
)

// drainingHandler prevents new admissions before waiting for every admitted
// request, including a handler still running after its connection was closed.
type drainingHandler struct {
	handler  http.Handler
	mu       sync.Mutex
	stopping bool
	active   sync.WaitGroup
}

func newDrainingHandler(handler http.Handler) *drainingHandler {
	return &drainingHandler{handler: handler}
}

func (d *drainingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	if d.stopping {
		d.mu.Unlock()
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.Error(w, "The server is shutting down.", http.StatusServiceUnavailable)
		return
	}
	d.active.Add(1)
	d.mu.Unlock()
	defer d.active.Done()
	d.handler.ServeHTTP(w, r)
}

func (d *drainingHandler) stop() {
	d.mu.Lock()
	d.stopping = true
	d.mu.Unlock()
}

// stop must precede wait, so no new WaitGroup additions can race with Wait.
func (d *drainingHandler) wait() { d.active.Wait() }
