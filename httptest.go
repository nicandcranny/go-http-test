package httptest

import (
	"bytes"
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

// Server is a mock http server for testing.
type Server struct {
	httpServer *http.Server
	engine     *echo.Echo
	config     ServerConfig
	// nCalls store map[method][path]count
	nCalls map[string]map[string]int
	// routes store map[method][path]handler
	routes map[string]map[string]ServerHandlerFunc
	calls  map[string]map[string][]RequestMade

	mu sync.Mutex
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
		engine:     e,
		config:     config,
		httpServer: httpServer,
		nCalls:     map[string]map[string]int{},
		routes:     map[string]map[string]ServerHandlerFunc{},
		calls:      map[string]map[string][]RequestMade{},
	}

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

// GetNCalls returns the number of nCalls for a path.
func (s *Server) GetNCalls(method, path string) int {
	calls, ok := s.nCalls[method][path]
	if !ok {
		return 0
	}

	return calls
}

// ResetNCalls resets the number of nCalls for all paths.
func (s *Server) ResetNCalls() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for path := range s.nCalls {
		for method := range s.nCalls[path] {
			s.nCalls[path][method] = 0
		}
	}
}

// GetCalls returns the calls for a path.
func (s *Server) GetCalls(method, path string) []RequestMade {
	return s.calls[method][path]
}

// ResetCalls resets the calls & nCalls for all paths.
// It does not reset the handlers.
func (s *Server) ResetCalls() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for path := range s.calls {
		for method := range s.calls[path] {
			s.calls[path][method] = []RequestMade{}
		}
	}
	// Reset NCalls too. We can't call ResetNCalls() here because it will try to acquire the lock too and cause a deadlock.
	for path := range s.nCalls {
		for method := range s.nCalls[path] {
			s.nCalls[path][method] = 0
		}
	}
}

// RegisterHandler registers handler of a path.
// Registering same path twice will overwrite the previous handler.
func (s *Server) RegisterHandler(method string, path string, handler ServerHandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.nCalls[method] == nil {
		s.nCalls[method] = map[string]int{}
	}
	if s.routes[method] == nil {
		s.routes[method] = map[string]ServerHandlerFunc{}
	}
	if s.calls[method] == nil {
		s.calls[method] = map[string][]RequestMade{}
	}

	_, alreadyRegistered := s.routes[method][path]
	s.routes[method][path] = handler

	if !alreadyRegistered {
		s.engine.Add(method, path, func(c echo.Context) error {
			s.incrNCalls(method, path)
			s.storeCall(method, path, c)

			s.mu.Lock()
			currHandler := s.routes[method][path]
			s.mu.Unlock()

			currHandler(ResponseWriter{w: c.Response().Writer}, &Request{Request: c.Request(), Params: Params{echoContext: c}})
			return nil
		})
	}
}

// incrNCalls increments the number of nCalls for a path.
func (s *Server) incrNCalls(method, path string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nCalls[method][path]++
}

// storeCall stores the call for a path.
func (s *Server) storeCall(method, path string, c echo.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// If body is not empty, read it into byte.
	var body []byte
	if c.Request().Body != nil {
		body, _ = io.ReadAll(c.Request().Body)
		// Restore the body.
		c.Request().Body = io.NopCloser(bytes.NewBuffer(body))
	}

	s.calls[method][path] = append(s.calls[method][path], RequestMade{
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

// ResetAll resets all the nCalls, handlers, and calls.
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

	s.nCalls = map[string]map[string]int{}
	s.routes = map[string]map[string]ServerHandlerFunc{}
	s.calls = map[string]map[string][]RequestMade{}
}
