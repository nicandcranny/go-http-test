# go-http-test: HTTP Testing Library for Go

go-http-test is a library for Go that provides a convenient way to start a new HTTP server at a desired address and enables you to perform various HTTP testing scenarios with ease.
This library is especially useful for writing unit tests and end-to-end tests for HTTP-based applications in Go.
This library also supports path parameter using `/:pathparam`.

## Features

- Start a new HTTP server at a custom address for testing purposes.
- Register custom handlers for different paths on the server.
- Track the number of calls made to specific paths on the server.
- Reset the call counters for individual paths, facilitating multiple test scenarios.
- Reregister handler same path will overwrite the previous handler.
- Reset all function to clear out the calls & handlers.
- **Two modes**: *simple mode* (one handler per route) for single/sequential
  tests or endpoint with static response, 
  and *keyed mode* (per-request buckets) for parallel tests that need a
  different response per request. See [Modes](#modes) and
  [Parallel Tests](#parallel-tests-keyed-mode).

## Installation

To use go-http-test in your Go projects, you need to have Go (>=1.23) installed and set up. Then, you can install the library using `go get`:

```bash
go get github.com/nicandcranny/go-http-test
```

## Modes

go-http-test can be used in two modes:

- **Simple mode** — one handler per route. Perfect for a single test (or a
  sequential suite): register a handler, make the request, assert the calls.
  This is also perfect for static endpoint that always returns the same result
  in every test.
  Use `RegisterHandler`, `GetNCalls`, `GetCalls`, `ResetCalls`.
- **Keyed mode** — one route, many per-request buckets. Use this when you want to
  **run tests in parallel against the same route and return a different response
  for different requests**. Each request is routed to a bucket by a key you
  derive from the request (e.g. a user id, a client id, a channel), so each
  parallel test owns its own handler and its own recorded calls with no
  cross-test interference. Use `RegisterKeyedRoute` + `RegisterKeyedHandler`,
  `GetNCallsByKey`, `GetCallsByKey`, `ResetCallsByKey`.

Simple mode is the shorthand; keyed mode is the parallel-safe path. The simple
mode methods are just keyed mode with a single default bucket, so you can mix
both and existing simple-mode code keeps working unchanged.

| Simple mode (single test)      | Keyed mode (parallel-safe)                     |
|--------------------------------|------------------------------------------------|
| `RegisterHandler`              | `RegisterKeyedRoute` + `RegisterKeyedHandler`  |
| `GetNCalls(method, path)`      | `GetNCallsByKey(method, path, key)`            |
| `GetCalls(method, path)`       | `GetCallsByKey(method, path, key)`             |
| `ResetCalls()` (all buckets)   | `ResetCallsByKey(method, path, key)`           |

The simple mode methods still work unchanged — they operate on a single default
bucket (the empty-string key), so existing single-test code needs no changes.

## Example Usage

To see real-life usage examples, check out [/examples](/examples):

- Simple mode: [send_slack_message_test.go](/examples/send_slack_message_test.go)
- Keyed/parallel mode: [send_slack_message_keyed_test.go](/examples/send_slack_message_keyed_test.go)

### Simple Mode

Here's a short example of how you can use go-http-test to test an HTTP endpoint that returns a predefined response:

```go
package main_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	httptest "github.com/nicandcranny/go-http-test"
	"github.com/stretchr/testify/assert"
)

func TestExample(t *testing.T) {
	// Start a new HTTP server using go-http-test
	server, err := httptest.NewServer("localhost:8080", httptest.ServerConfig{
		EnableLogging: true, // To enable logging
	})
	assert.NoError(t, err)
	defer server.Close()

	path := "/some-path/:id"
	expectedResBody := []byte(`{"res":"ponse"}`)

	// You can also return a JSON by using a struct with json tag or map[string]any.
	type resStruct struct {
		Abcd string `json:"abcd"`
		Efgh int    `json:"efgh"`
	}
	server.RegisterHandler(http.MethodPost, path, func(w httptest.ResponseWriter, r *httptest.Request) {
		// You can do validation for the request here, e.g. request header, body, etc
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "1d", r.Params.ByName("id"))

		reqBodyByte, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		var reqBody map[string]any
		assert.NoError(t, json.Unmarshal(reqBodyByte, &reqBody))
		assert.Equal(t, "abcd", reqBody["abcd"])

		// You can also generate different response body based on the request body
		w.Header().Set("Content-Type", "application/json")
		w.SetStatusCode(http.StatusOK)
		w.SetBodyJSON(resStruct{
			Abcd: reqBody["abcd"],
			Efgh: 1,
		})

		// Or you can also send using this method to simplify the code
		w.JSON(http.StatusOK, resStruct{
			Abcd: reqBody["abcd"],
			Efgh: 1,
		})
	})

	// Test doing a GET request to the path
	res, err := http.Get("http://localhost:8080/some-path/1d")
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, res.StatusCode)

    // Assert total number of call to the path
	assert.Equal(t, 1, server.GetNCalls(http.MethodGet, path))

	// Reset the call counters back to 0
	server.ResetNCalls()

	// We can also get the call and assert here.
	call := server.GetCalls(http.MethodPost, path)[0]
	assert.Equal(t, "1d", call.Params["1d"])
}
```

### Keyed Mode

Keyed mode buckets all per-route state under a **key derived from each request**
— `map[method][path][key]` — so parallel tests can share a route while each owns
its own handler, response, and recorded calls.

There are two steps:

1. `RegisterKeyedRoute(method, path, keyFn)` — declare the route **once** (e.g. in
   suite setup). The `keyFn` extracts the bucket key from each incoming request.
   The first registration for a `(method, path)` wins; later calls are no-ops, so
   concurrent setup from parallel tests is safe. The request body is buffered and
   restored, so reading it inside `keyFn` does not consume it for the handler.
2. `RegisterKeyedHandler(method, path, key, handler)` — each test registers its
   own handler under its own key. Registering the same key again overwrites only
   that key's handler; other keys are untouched.

Reads are per-key (`GetNCallsByKey`, `GetCallsByKey`, `ResetCallsByKey`) and only
touch that test's bucket. Requests whose key has no registered handler get a
`404` (the call is still recorded under that key).

```go
func TestNotifyUsers(t *testing.T) {
	t.Parallel()

	server, err := httptest.NewServer("127.0.0.1:8080", httptest.ServerConfig{})
	assert.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })

	// Declare the route once. Bucket each request by its "user_id".
	server.RegisterKeyedRoute(http.MethodPost, "/notify", func(r *httptest.Request) string {
		body, _ := io.ReadAll(r.Body) // body is buffered & restored for the handler
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		id, _ := m["user_id"].(string)
		return id
	})

	for _, userID := range []string{"u-aaa", "u-bbb", "u-ccc"} {
		userID := userID
		t.Run(userID, func(t *testing.T) {
			t.Parallel()

			// Each parallel test owns its own key's handler + response.
			server.RegisterKeyedHandler(http.MethodPost, "/notify", userID,
				func(w httptest.ResponseWriter, r *httptest.Request) {
					w.JSON(http.StatusOK, map[string]string{"user_id": userID})
				},
			)

			body := []byte(fmt.Sprintf(`{"user_id":%q}`, userID))
			res, _ := http.Post("http://"+server.Addr()+"/notify", "application/json", bytes.NewReader(body))
			assert.Equal(t, http.StatusOK, res.StatusCode)

			// Reads are scoped to this test's key only.
			assert.Equal(t, 1, server.GetNCallsByKey(http.MethodPost, "/notify", userID))
			calls := server.GetCallsByKey(http.MethodPost, "/notify", userID)
			assert.Len(t, calls, 1)
		})
	}
}
```

See [send_slack_message_keyed_test.go](/examples/send_slack_message_keyed_test.go)
for a runnable example that posts to several Slack channels in parallel, keyed by
channel, each expecting its own permalink response.

## Contributing

go-http-test is an open source project, and we welcome contributions from the community. If you find a bug, have an enhancement in mind, or want to propose a new feature, please open an issue or submit a pull request on the GitHub repository.

Happy testing with go-http-test! If you have any questions or need further assistance, feel free to reach out to the project maintainers.
