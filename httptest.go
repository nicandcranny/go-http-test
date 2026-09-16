package httptest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

// defaultKey is the bucket key used by the keyless methods (RegisterHandler,
// GetNCalls, GetCalls, ...). Keyless usage is single-test only: every request
// to a route lands in this single bucket, so it is NOT safe to share one such
// route across tests running in parallel.
//
// For parallel tests, use the keyed API (RegisterKeyedHandler, GetNCallsByKey,
// GetCallsByKey, ...) so each test's requests are isolated into their own bucket
// derived from a per-request unique key (e.g. a merchant id or client id).
const defaultKey = ""

// Server is a mock http server for testing.
type Server struct {
	httpServer   *http.Server
	engine       *echo.Echo
	config       ServerConfig
	listenerAddr string

	// nCalls stores the call count as map[method][path][key]count.
	nCalls map[string]map[string]map[string]int
	// handlers stores the registered handler as map[method][path][key]handler.
	// Each (method, path) has exactly one echo route; that route dispatches to
	// the per-key handler using the route's keyFn.
	handlers map[string]map[string]map[string]ServerHandlerFunc
	// keyFns stores the key extractor per route as map[method][path]keyFn.
	// The keyFn maps an incoming request to the bucket key that selects the
	// handler and the call log. Keyless routes use a keyFn that always returns
	// defaultKey.
	keyFns map[string]map[string]KeyFunc
	// calls stores the recorded calls as map[method][path][key]calls.
	calls map[string]map[string]map[string][]RequestMade

	mu sync.Mutex

	// inFlightMu guards inFlight and backs inFlightCond. It is deliberately
	// separate from mu so that a running handler (which is counted as in-flight)
	// never blocks route registration/inspection, and vice versa.
	inFlightMu   sync.Mutex
	inFlightCond *sync.Cond
	// inFlight is the number of user handlers currently executing across all
	// routes. See InFlight and WaitUntilIdle.
	inFlight int
}

type Request struct {
	*http.Request
	Params Params
}

type RequestMade struct {
	Body    []byte
	Headers http.Header
	Query   url.Values
	Params  map[string]string
}

// ServerHandlerFunc is the interface of the handler function.
type ServerHandlerFunc func(w ResponseWriter, r *Request)

// KeyFunc derives the bucket key for an incoming request. Requests that return
// the same key share a handler and a call log. Return a value that is unique
// per test (e.g. a per-test credential embedded in the request) so parallel
// tests hitting the same route stay isolated.
type KeyFunc func(r *Request) string

type ServerConfig struct {
	EnableLogging bool
}

// NewServer creates and starts new http test server.
// address is the address to listen on, e.g. "localhost:3001".
func NewServer(address string, config ServerConfig) (*Server, error) {
	// Start listener first to make sure the address is available.
	l, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("net.Listen: %w", err)
	}

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true

	if config.EnableLogging {
		e.Use(middleware.RequestLogger())
	}

	httpServer := &http.Server{
		Addr:    address,
		Handler: e,
	}

	server := &Server{
		engine:       e,
		config:       config,
		httpServer:   httpServer,
		listenerAddr: l.Addr().String(),
		nCalls:       map[string]map[string]map[string]int{},
		handlers:     map[string]map[string]map[string]ServerHandlerFunc{},
		keyFns:       map[string]map[string]KeyFunc{},
		calls:        map[string]map[string]map[string][]RequestMade{},
	}
	server.inFlightCond = sync.NewCond(&server.inFlightMu)

	go func() {
		if err = httpServer.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			panic(err)
		}
	}()

	return server, nil
}

// Close closes the server.
func (s *Server) Close() error {
	return s.httpServer.Close()
}

// Addr returns the actual network address the server is listening on. This is
// useful when NewServer was called with a ":0" port so the OS assigns a free
// port — each parallel test can then spin up its own isolated server without
// hardcoding (and colliding on) a fixed port.
func (s *Server) Addr() string {
	return s.listenerAddr
}

// enterHandler records that a user handler has started executing.
func (s *Server) enterHandler() {
	s.inFlightMu.Lock()
	s.inFlight++
	s.inFlightMu.Unlock()
}

// exitHandler records that a user handler has finished executing and wakes any
// goroutine waiting in WaitUntilIdle.
func (s *Server) exitHandler() {
	s.inFlightMu.Lock()
	s.inFlight--
	s.inFlightCond.Broadcast()
	s.inFlightMu.Unlock()
}

// InFlight returns the number of request handlers currently executing on the
// server. It counts only the user handler invocation, not connection setup or
// call recording. A return value of 0 means no handler is running at the instant
// of the call (a new request may begin immediately after).
func (s *Server) InFlight() int {
	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	return s.inFlight
}

// WaitUntilIdle blocks until no request handler is executing, or until ctx is
// done, whichever happens first. It returns nil once the server is idle, or
// ctx.Err() if the context is cancelled or its deadline passes first.
//
// This waits only for the requests already in flight (plus any that begin while
// waiting) to finish; it does not stop the server from accepting new requests
// and makes no guarantee about requests that arrive after it returns.
//
// It is useful when the system under test calls this mock server from
// background goroutines that may outlive the synchronous body of a test: wait
// for those handlers to complete before moving on, so a still-running handler
// cannot race with the next test's setup.
func (s *Server) WaitUntilIdle(ctx context.Context) error {
	// Wake the waiter when ctx is done so it does not block past cancellation.
	stop := context.AfterFunc(ctx, func() {
		s.inFlightMu.Lock()
		s.inFlightCond.Broadcast()
		s.inFlightMu.Unlock()
	})
	defer stop()

	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	for s.inFlight > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.inFlightCond.Wait()
	}
	return nil
}

// GetNCalls returns the number of calls for a path in the default (keyless) bucket.
// For parallel tests, use GetNCallsByKey.
func (s *Server) GetNCalls(method, path string) int {
	return s.GetNCallsByKey(method, path, defaultKey)
}

// GetNCallsByKey returns the number of calls for a path in the given key bucket.
func (s *Server) GetNCallsByKey(method, path, key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.nCalls[method][path][key]
}

// ResetNCalls resets the number of calls for all paths and all keys.
func (s *Server) ResetNCalls() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.resetNCallsLocked()
}

// resetNCallsLocked zeroes every call counter. Caller must hold s.mu.
func (s *Server) resetNCallsLocked() {
	for method := range s.nCalls {
		for path := range s.nCalls[method] {
			for key := range s.nCalls[method][path] {
				s.nCalls[method][path][key] = 0
			}
		}
	}
}

// GetCalls returns the recorded calls for a path in the default (keyless) bucket.
// For parallel tests, use GetCallsByKey.
func (s *Server) GetCalls(method, path string) []RequestMade {
	return s.GetCallsByKey(method, path, defaultKey)
}

// GetCallsByKey returns the recorded calls for a path in the given key bucket.
func (s *Server) GetCallsByKey(method, path, key string) []RequestMade {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.calls[method][path][key]
}

// ResetCalls resets the calls & nCalls for all paths and all keys.
// It does not reset the handlers.
func (s *Server) ResetCalls() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for method := range s.calls {
		for path := range s.calls[method] {
			for key := range s.calls[method][path] {
				s.calls[method][path][key] = []RequestMade{}
			}
		}
	}
	// Reset NCalls too. We can't call ResetNCalls() here because it would try
	// to acquire the lock again and deadlock.
	s.resetNCallsLocked()
}

// ResetCallsByKey resets the calls & nCalls for a single path/key bucket.
// It does not reset the handler. Use this to reset just the current test's
// bucket without touching other parallel tests' state.
func (s *Server) ResetCallsByKey(method, path, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.calls[method][path]; ok {
		s.calls[method][path][key] = []RequestMade{}
	}
	if _, ok := s.nCalls[method][path]; ok {
		s.nCalls[method][path][key] = 0
	}
}

// RegisterHandler registers a handler for a path in the default (keyless) bucket.
// Registering the same path twice overwrites the previous handler.
//
// This is single-test only. For tests that run in parallel and share a route,
// use RegisterKeyedRoute + RegisterKeyedHandler so each test's requests are
// routed to an isolated bucket.
func (s *Server) RegisterHandler(method string, path string, handler ServerHandlerFunc) {
	s.RegisterKeyedRoute(method, path, func(*Request) string { return defaultKey })
	s.RegisterKeyedHandler(method, path, defaultKey, handler)
}

// RegisterKeyedRoute declares a route whose incoming requests are bucketed by
// the key that keyFn derives from each request. Call it once per (method, path)
// during suite setup, before any parallel tests run.
//
// The keyFn is fixed on the first call for a route; later calls are no-ops (so
// concurrent registrations from parallel tests are safe and agree on how the
// bucket key is derived). Requests whose keyFn result has no registered handler
// get a 404.
//
// Per-test handlers and responses are then registered against a specific key
// via RegisterKeyedHandler, and each test reads back only its own bucket via
// GetNCallsByKey / GetCallsByKey.
func (s *Server) RegisterKeyedRoute(method string, path string, keyFn KeyFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ensureRouteMapsLocked(method, path)

	if _, routeExists := s.keyFns[method][path]; routeExists {
		// Route (and its echo handler) already registered; keep the first keyFn.
		return
	}
	s.keyFns[method][path] = keyFn

	s.engine.Add(method, path, func(c echo.Context) error {
		// Read the body once up front and buffer it. The keyFn, the call
		// recorder, and the user handler all need to read the body, so we
		// restore a fresh reader before each consumer.
		var rawBody []byte
		if c.Request().Body != nil {
			rawBody, _ = io.ReadAll(c.Request().Body)
		}
		restoreBody := func() {
			c.Request().Body = io.NopCloser(bytes.NewReader(rawBody))
		}

		restoreBody()
		req := &Request{Request: c.Request(), Params: Params{echoContext: c}}

		s.mu.Lock()
		routeKeyFn := s.keyFns[method][path]
		s.mu.Unlock()

		key := routeKeyFn(req)

		s.incrNCalls(method, path, key)
		s.storeCall(method, path, key, rawBody, c)

		s.mu.Lock()
		currHandler := s.handlers[method][path][key]
		s.mu.Unlock()

		if currHandler == nil {
			// No handler registered for this key. Return 404 so the caller gets
			// a clear signal that this bucket was never set up, instead of a
			// nil-deref panic.
			return c.NoContent(http.StatusNotFound)
		}

		// Give the user handler a fresh, full body to read.
		restoreBody()
		s.enterHandler()
		defer s.exitHandler()
		currHandler(ResponseWriter{w: c.Response().Writer}, req)
		return nil
	})
}

// RegisterKeyedHandler registers the handler that serves requests whose derived
// key equals the given key. The route must already be declared with
// RegisterKeyedRoute. Registering the same (method, path, key) again overwrites
// only that key's handler; other keys' handlers are untouched, which is what
// lets parallel tests each own their own bucket on a shared route.
func (s *Server) RegisterKeyedHandler(
	method string,
	path string,
	key string,
	handler ServerHandlerFunc,
) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.ensureRouteMapsLocked(method, path)
	s.handlers[method][path][key] = handler
}

// ensureRouteMapsLocked lazily initializes the nested maps for a route.
// Caller must hold s.mu.
func (s *Server) ensureRouteMapsLocked(method, path string) {
	if s.nCalls[method] == nil {
		s.nCalls[method] = map[string]map[string]int{}
	}
	if s.nCalls[method][path] == nil {
		s.nCalls[method][path] = map[string]int{}
	}
	if s.handlers[method] == nil {
		s.handlers[method] = map[string]map[string]ServerHandlerFunc{}
	}
	if s.handlers[method][path] == nil {
		s.handlers[method][path] = map[string]ServerHandlerFunc{}
	}
	if s.calls[method] == nil {
		s.calls[method] = map[string]map[string][]RequestMade{}
	}
	if s.calls[method][path] == nil {
		s.calls[method][path] = map[string][]RequestMade{}
	}
	if s.keyFns[method] == nil {
		s.keyFns[method] = map[string]KeyFunc{}
	}
}

// incrNCalls increments the number of calls for a path/key bucket.
func (s *Server) incrNCalls(method, path, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.nCalls[method][path] == nil {
		s.nCalls[method][path] = map[string]int{}
	}
	s.nCalls[method][path][key]++
}

// storeCall records the call for a path/key bucket. body is the already-read
// request body (buffered by the route dispatch) so we don't consume the reader
// the user handler still needs.
func (s *Server) storeCall(method, path, key string, body []byte, c echo.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.calls[method][path] == nil {
		s.calls[method][path] = map[string][]RequestMade{}
	}
	s.calls[method][path][key] = append(s.calls[method][path][key], RequestMade{
		Body:    body,
		Headers: c.Request().Header,
		Query:   c.QueryParams(),
		Params:  s.getAllParams(c),
	})
}

func (s *Server) getAllParams(c echo.Context) map[string]string {
	params := make(map[string]string)

	names := c.ParamNames()
	values := c.ParamValues()

	for i, name := range names {
		if i < len(values) {
			params[name] = values[i]
		}
	}

	return params
}

// ResetAll resets all the nCalls, handlers, keyFns, and calls.
func (s *Server) ResetAll() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.engine = echo.New()
	s.engine.HideBanner = true
	s.engine.HidePort = true

	if s.config.EnableLogging {
		s.engine.Use(middleware.RequestLogger())
	}

	s.httpServer.Handler = s.engine

	s.nCalls = map[string]map[string]map[string]int{}
	s.handlers = map[string]map[string]map[string]ServerHandlerFunc{}
	s.keyFns = map[string]map[string]KeyFunc{}
	s.calls = map[string]map[string]map[string][]RequestMade{}
}
