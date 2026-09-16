package httptest_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	httptest "github.com/nicandcranny/go-http-test"
	"github.com/nicandcranny/go-http-test/internal/httpclient"
)

const baseURL = "http://127.0.0.1:3010"
const address = "127.0.0.1:3010"

var (
	ctx = context.Background()
)

// newTestClient builds an http client helper pointed at baseURL, used by the
// keyed/parallel tests (which run via t.Run rather than the suite).
func newTestClient(baseURL string) *httpclient.HttpClient {
	c := &http.Client{Timeout: 2 * time.Second}
	return httpclient.New(baseURL, c)
}

const inflightAddress = "127.0.0.1:3011"
const inflightBaseURL = "http://127.0.0.1:3011"

// TestInFlightAndWaitUntilIdle verifies that InFlight reflects a running handler
// and that WaitUntilIdle blocks until the handler finishes.
func TestInFlightAndWaitUntilIdle(t *testing.T) {
	server, err := httptest.NewServer(inflightAddress, httptest.ServerConfig{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()

	if got := server.InFlight(); got != 0 {
		t.Fatalf("InFlight before any request = %d, want 0", got)
	}

	// release lets the handler finish only when the test allows it.
	release := make(chan struct{})
	entered := make(chan struct{})
	server.RegisterHandler(http.MethodGet, "/slow", func(w httptest.ResponseWriter, r *httptest.Request) {
		close(entered)
		<-release
		w.SetStatusCode(http.StatusOK)
	})

	client := newTestClient(inflightBaseURL)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _, _ = client.Do(context.Background(), http.MethodGet, "/slow", nil, nil, nil)
	}()

	// Wait until the handler is actually executing.
	<-entered
	if got := server.InFlight(); got != 1 {
		t.Fatalf("InFlight while handler running = %d, want 1", got)
	}

	// WaitUntilIdle must NOT return while the handler is still in flight.
	idleReturned := make(chan error, 1)
	go func() {
		idleReturned <- server.WaitUntilIdle(context.Background())
	}()
	select {
	case <-idleReturned:
		t.Fatal("WaitUntilIdle returned while a handler was still in flight")
	case <-time.After(100 * time.Millisecond):
		// expected: still blocking
	}

	// Let the handler complete; WaitUntilIdle should now return nil.
	close(release)
	select {
	case err := <-idleReturned:
		if err != nil {
			t.Fatalf("WaitUntilIdle returned error after idle: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitUntilIdle did not return after the handler finished")
	}

	wg.Wait()
	if got := server.InFlight(); got != 0 {
		t.Fatalf("InFlight after handler finished = %d, want 0", got)
	}
}

// TestWaitUntilIdleContextCancelled verifies WaitUntilIdle returns the context
// error if the handler never finishes within the deadline.
func TestWaitUntilIdleContextCancelled(t *testing.T) {
	server, err := httptest.NewServer(inflightAddress, httptest.ServerConfig{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()

	release := make(chan struct{})
	entered := make(chan struct{})
	server.RegisterHandler(http.MethodGet, "/hang", func(w httptest.ResponseWriter, r *httptest.Request) {
		close(entered)
		<-release
	})
	defer close(release)

	client := newTestClient(inflightBaseURL)
	go func() { _, _, _ = client.Do(context.Background(), http.MethodGet, "/hang", nil, nil, nil) }()

	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = server.WaitUntilIdle(ctx)
	if err == nil {
		t.Fatal("WaitUntilIdle returned nil, want context deadline error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("WaitUntilIdle blocked too long (%s) past the context deadline", elapsed)
	}
}

type serverTestSuite struct {
	suite.Suite
	httpClient *httpclient.HttpClient
}

func TestServerTestSuite(t *testing.T) {
	httpClient := http.DefaultClient
	httpClient.Timeout = 500 * time.Millisecond
	suite.Run(t, &serverTestSuite{
		httpClient: httpclient.New(baseURL, httpClient),
	})
}

func (s *serverTestSuite) TestNotRegisteredPath() {
	server, err := httptest.NewServer(address, httptest.ServerConfig{})
	s.NoError(err)
	defer server.Close()

	res, _, err := s.httpClient.Do(
		ctx,
		http.MethodGet,
		"/unknown",
		nil,
		nil,
		nil,
	)
	s.NoError(err)
	s.Equal(http.StatusNotFound, res.StatusCode)
}

func (s *serverTestSuite) TestPost_ResponseJSON() {
	server, err := httptest.NewServer(address, httptest.ServerConfig{})
	s.NoError(err)
	defer server.Close()

	path := "/some-path/:id"
	expectedReqBody := []byte(`{"test":"osterone"}`)
	expectedResBody := []byte(`{"abcd":"abcd","efgh":1}`)

	type resStruct struct {
		Abcd string `json:"abcd"`
		Efgh int    `json:"efgh"`
	}

	server.RegisterHandler(http.MethodPost, path, func(w httptest.ResponseWriter, r *httptest.Request) {
		s.Equal(http.MethodPost, r.Method)
		s.Equal("/some-path/123", r.URL.Path)

		reqBody, err := io.ReadAll(r.Body)
		s.NoError(err)
		s.Equal(expectedReqBody, reqBody)

		w.SetStatusCode(http.StatusOK)
		w.SetBodyJSON(resStruct{
			Abcd: "abcd",
			Efgh: 1,
		})
	})

	res, resBody, err := s.httpClient.Do(
		ctx,
		http.MethodPost,
		"/some-path/123?abcd=def",
		map[string]string{
			"Content-Type": "application/json",
		},
		expectedReqBody,
		nil,
	)
	s.NoError(err)

	s.Equal(200, res.StatusCode)
	s.Equal(expectedResBody, resBody)

	s.Equal(1, server.GetNCalls(http.MethodPost, path))

	// Get Call.
	calls := server.GetCalls(http.MethodPost, path)
	s.Equal(1, len(calls))
	s.Equal(expectedReqBody, calls[0].Body)
	s.Equal(url.Values{
		"abcd": {"def"},
	}, calls[0].Query)
	s.Equal(map[string]string{
		"id": "123",
	}, calls[0].Params)
	s.Equal("application/json", calls[0].Headers.Get("Content-Type"))
}

func (s *serverTestSuite) TestGet_WithQueryParams() {
	// Should only register the path without query params.
	server, err := httptest.NewServer(address, httptest.ServerConfig{})
	s.NoError(err)
	defer server.Close()

	path := "/some-path"
	expectedResBody := []byte(`{"res":"ponse"}`)

	server.RegisterHandler(http.MethodGet, path, func(w httptest.ResponseWriter, r *httptest.Request) {
		s.Equal("param", r.FormValue("query"))
		s.Equal(http.MethodGet, r.Method)
		s.Equal(path, r.URL.Path)

		w.SetStatusCode(http.StatusOK)
		w.SetBodyBytes(expectedResBody)
	})

	qParams := url.Values{}
	qParams.Add("query", "param")
	qParams.Add("test", "osterone")

	res, resBody, err := s.httpClient.Do(
		ctx,
		http.MethodGet,
		path,
		nil,
		nil,
		qParams,
	)
	s.NoError(err)

	s.Equal(200, res.StatusCode)
	s.Equal(expectedResBody, resBody)

	s.Equal(1, server.GetNCalls(http.MethodGet, path))
}

func (s *serverTestSuite) TestGet_WithPathParam() {
	// Should only register the path without query params.
	server, err := httptest.NewServer(address, httptest.ServerConfig{})
	s.NoError(err)
	defer server.Close()

	path := "/some-path/:id/subpath/:subid"
	expectedResBody := []byte(`{"res":"ponse"}`)

	server.RegisterHandler(http.MethodGet, path, func(w httptest.ResponseWriter, r *httptest.Request) {
		s.Equal("1d", r.Params.ByName("id"))
		s.Equal("5ub1d", r.Params.ByName("subid"))
		s.Equal(http.MethodGet, r.Method)

		w.SetStatusCode(http.StatusOK)
		w.SetBodyBytes(expectedResBody)
	})

	reqPath := "/some-path/1d/subpath/5ub1d"

	res, resBody, err := s.httpClient.Do(
		ctx,
		http.MethodGet,
		reqPath,
		nil,
		nil,
		nil,
	)
	s.NoError(err)

	s.Equal(200, res.StatusCode)
	s.Equal(expectedResBody, resBody)

	s.Equal(1, server.GetNCalls(http.MethodGet, path))
}

func (s *serverTestSuite) TestGet_MultipleTimes_ResetNCalls() {
	server, err := httptest.NewServer(address, httptest.ServerConfig{})
	s.NoError(err)
	defer server.Close()

	path := "/some-path"
	expectedResBody := []byte(`{"res":"ponse"}`)

	server.RegisterHandler(http.MethodGet, path, func(w httptest.ResponseWriter, r *httptest.Request) {
		w.SetStatusCode(http.StatusOK)
		w.SetBodyBytes(expectedResBody)
	})

	qParams := url.Values{}
	qParams.Add("query", "param")
	qParams.Add("test", "osterone")

	for i := 0; i < 10; i++ {
		res, resBody, err := s.httpClient.Do(
			ctx,
			http.MethodGet,
			path,
			nil,
			nil,
			qParams,
		)
		s.NoError(err)
		s.Equal(200, res.StatusCode)
		s.Equal(expectedResBody, resBody)
	}

	s.Equal(10, server.GetNCalls(http.MethodGet, path))
	server.ResetNCalls()
	s.Equal(0, server.GetNCalls(http.MethodGet, path))
}

func (s *serverTestSuite) TestReregisterHandler_ShouldOverwrite() {
	server, err := httptest.NewServer(address, httptest.ServerConfig{})
	s.NoError(err)
	defer server.Close()

	path := "/some-path/subpath"
	path2 := "/some-path2"
	expectedResBody := []byte(`{"res":"ponse"}`)

	server.RegisterHandler(http.MethodGet, path, func(w httptest.ResponseWriter, r *httptest.Request) {
		w.SetStatusCode(http.StatusOK)
		w.SetBodyBytes(expectedResBody)
	})

	server.RegisterHandler(http.MethodPost, path2, func(w httptest.ResponseWriter, r *httptest.Request) {
		w.SetStatusCode(http.StatusCreated)
		w.SetBodyBytes(expectedResBody)
	})

	server.RegisterHandler(http.MethodGet, path, func(w httptest.ResponseWriter, r *httptest.Request) {
		w.Header().Set("some", "header")
		w.SetStatusCode(http.StatusNotFound)
	})

	res, _, err := s.httpClient.Do(
		ctx,
		http.MethodGet,
		path,
		nil,
		nil,
		nil,
	)
	s.NoError(err)
	s.Equal(404, res.StatusCode)
	s.Equal("header", res.Header.Get("some"))

	s.Equal(1, server.GetNCalls(http.MethodGet, path))

	// Make sure that other path is still working.
	res, _, err = s.httpClient.Do(
		ctx,
		http.MethodPost,
		path2,
		nil,
		nil,
		nil,
	)
	s.NoError(err)
	s.Equal(201, res.StatusCode)
	s.Equal(1, server.GetNCalls(http.MethodPost, path2))
}

func (s *serverTestSuite) TestSimulateTimeout() {
	server, err := httptest.NewServer(address, httptest.ServerConfig{})
	s.NoError(err)
	defer server.Close()

	path := "/some-path"
	expectedResBody := []byte(`{"res":"ponse"}`)

	server.RegisterHandler(http.MethodGet, path, func(w httptest.ResponseWriter, r *httptest.Request) {
		time.Sleep(1 * time.Second)
		w.SetStatusCode(http.StatusOK)
		w.SetBodyBytes(expectedResBody)
	})

	_, _, err = s.httpClient.Do(
		ctx,
		http.MethodGet,
		path,
		nil,
		nil,
		nil,
	)
	s.Error(err)
	s.True(os.IsTimeout(err))

	s.Equal(1, server.GetNCalls(http.MethodGet, path))
}

func (s *serverTestSuite) TestResetAll() {
	server, err := httptest.NewServer(address, httptest.ServerConfig{})
	s.NoError(err)
	defer server.Close()

	path := "/some-path/subpath"
	expectedResBody := []byte(`{"res":"ponse"}`)

	server.RegisterHandler(http.MethodGet, path, func(w httptest.ResponseWriter, r *httptest.Request) {
		w.SetStatusCode(http.StatusOK)
		w.SetBodyBytes(expectedResBody)
	})

	server.ResetAll()

	res, _, err := s.httpClient.Do(
		ctx,
		http.MethodGet,
		path,
		nil,
		nil,
		nil,
	)
	s.NoError(err)
	s.Equal(http.StatusNotFound, res.StatusCode)

	s.Equal(0, server.GetNCalls(http.MethodGet, path))
	s.Equal(0, len(server.GetCalls(http.MethodGet, path)))
}

func (s *serverTestSuite) TestServerWithLogging() {
	server, err := httptest.NewServer(address, httptest.ServerConfig{
		EnableLogging: true,
	})
	s.NoError(err)
	defer server.Close()

	path := "/log-path"
	server.RegisterHandler(http.MethodGet, path, func(w httptest.ResponseWriter, r *httptest.Request) {
		w.SetStatusCode(http.StatusOK)
		w.SetBodyBytes([]byte("ok"))
	})

	res, _, err := s.httpClient.Do(ctx, http.MethodGet, path, nil, nil, nil)
	s.NoError(err)
	s.Equal(http.StatusOK, res.StatusCode)
}

func (s *serverTestSuite) TestResponseWriter_NewMethods() {
	server, err := httptest.NewServer(address, httptest.ServerConfig{})
	s.NoError(err)
	defer server.Close()

	// Test JSON
	server.RegisterHandler(http.MethodGet, "/json", func(w httptest.ResponseWriter, r *httptest.Request) {
		w.JSON(http.StatusOK, map[string]string{"foo": "bar"})
	})

	// Test HTML
	server.RegisterHandler(http.MethodGet, "/html", func(w httptest.ResponseWriter, r *httptest.Request) {
		w.HTML(http.StatusOK, "<h1>Hello</h1>")
	})

	// Test String
	server.RegisterHandler(http.MethodGet, "/string", func(w httptest.ResponseWriter, r *httptest.Request) {
		w.String(http.StatusOK, "Hello World")
	})

	// Test NoContent
	server.RegisterHandler(http.MethodGet, "/nocontent", func(w httptest.ResponseWriter, r *httptest.Request) {
		w.NoContent(http.StatusNoContent)
	})

	// Verify JSON
	res, resBody, err := s.httpClient.Do(ctx, http.MethodGet, "/json", nil, nil, nil)
	s.NoError(err)
	s.Equal(http.StatusOK, res.StatusCode)
	s.Equal("application/json", res.Header.Get("Content-Type"))
	s.JSONEq(`{"foo":"bar"}`, string(resBody))

	// Verify HTML
	res, resBody, err = s.httpClient.Do(ctx, http.MethodGet, "/html", nil, nil, nil)
	s.NoError(err)
	s.Equal(http.StatusOK, res.StatusCode)
	s.Contains(res.Header.Get("Content-Type"), "text/html")
	s.Equal("<h1>Hello</h1>", string(resBody))

	// Verify String
	res, resBody, err = s.httpClient.Do(ctx, http.MethodGet, "/string", nil, nil, nil)
	s.NoError(err)
	s.Equal(http.StatusOK, res.StatusCode)
	s.Contains(res.Header.Get("Content-Type"), "text/plain")
	s.Equal("Hello World", string(resBody))

	// Verify NoContent
	res, _, err = s.httpClient.Do(ctx, http.MethodGet, "/nocontent", nil, nil, nil)
	s.NoError(err)
	s.Equal(http.StatusNoContent, res.StatusCode)
}
