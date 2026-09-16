# go-http-test

`go-http-test` starts a real HTTP server for Go tests. Register mock routes,
return controlled responses, and inspect the requests your code sent.

It supports path parameters, recorded request bodies and headers, parallel-test
isolation, and dynamically assigned ports.

## Install

Requires Go 1.26 or newer.

```bash
go get github.com/nicandcranny/go-http-test
```

## Quick start

Use port `0` to let the operating system choose an available port. `Addr`
returns the address selected for the server.

```go
package example_test

import (
	"net/http"
	"strings"
	"testing"

	mockhttp "github.com/nicandcranny/go-http-test"
)

func TestCreateUser(t *testing.T) {
	server, err := mockhttp.NewServer("127.0.0.1:0", mockhttp.ServerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })

	const route = "/users/:id"
	server.RegisterHandler(http.MethodPost, route, func(w mockhttp.ResponseWriter, r *mockhttp.Request) {
		if got := r.Params.ByName("id"); got != "42" {
			t.Errorf("id = %q, want 42", got)
		}
		_ = w.JSON(http.StatusCreated, map[string]any{"ok": true})
	})

	res, err := http.Post(
		"http://"+server.Addr()+"/users/42",
		"application/json",
		strings.NewReader(`{"name":"Ada"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusCreated)
	}

	calls := server.GetCalls(http.MethodPost, route)
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if got := calls[0].Params["id"]; got != "42" {
		t.Errorf("recorded id = %q, want 42", got)
	}
}
```

Handlers receive the standard `*http.Request` through `Request`, plus
`Params.ByName` for route parameters. Recorded calls contain:

- `Body []byte`
- `Headers http.Header`
- `Query url.Values`
- `Params map[string]string`

## Simple and keyed modes

Choose one mode per route:

| Mode | Use when | Registration | Assertions |
| --- | --- | --- | --- |
| Simple | Tests are sequential, or every request gets the same response | `RegisterHandler` | `GetNCalls`, `GetCalls` |
| Keyed | Parallel tests share a route but need isolated handlers and call logs | `RegisterKeyedRoute`, then `RegisterKeyedHandler` | `GetNCallsByKey`, `GetCallsByKey` |

### Simple mode

`RegisterHandler` assigns one handler to a method and path. Registering the same
route again replaces its handler. Do not let parallel tests replace a shared
route's handler; use keyed mode instead.

```go
server.RegisterHandler(http.MethodGet, "/health", func(w mockhttp.ResponseWriter, _ *mockhttp.Request) {
	_ = w.String(http.StatusOK, "ok")
})

count := server.GetNCalls(http.MethodGet, "/health")
calls := server.GetCalls(http.MethodGet, "/health")
```

### Keyed mode

Keyed mode gives each request a bucket. Each bucket has its own handler, call
count, and recorded calls, so parallel tests can share one server and route
without overwriting each other's state.

Declare the route once during setup. The first registration for a method and
path defines its key function; later registrations do nothing.

```go
server.RegisterKeyedRoute(http.MethodPost, "/notify", func(r *mockhttp.Request) string {
	return r.Header.Get("X-Test-ID")
})
```

Each test then registers a handler under a unique key and sends that key with
its request:

```go
const key = "test-1"

server.RegisterKeyedHandler(http.MethodPost, "/notify", key,
	func(w mockhttp.ResponseWriter, _ *mockhttp.Request) {
		_ = w.NoContent(http.StatusAccepted)
	},
)

req, err := http.NewRequest(http.MethodPost, "http://"+server.Addr()+"/notify", nil)
if err != nil {
	t.Fatal(err)
}
req.Header.Set("X-Test-ID", key)

res, err := http.DefaultClient.Do(req)
if err != nil {
	t.Fatal(err)
}
defer res.Body.Close()

calls := server.GetCallsByKey(http.MethodPost, "/notify", key)
```

Requests with no handler for their derived key receive `404 Not Found`, but the
request is still recorded. A key function may read the request body; the server
restores it before calling the registered handler.

See the runnable [simple example](/examples/send_slack_message_test.go) and
[parallel keyed example](/examples/send_slack_message_keyed_test.go).

## Resetting state

| Method | Clears |
| --- | --- |
| `ResetNCalls()` | All call counts |
| `ResetCalls()` | All recorded calls and call counts |
| `ResetCallsByKey(method, path, key)` | Recorded calls and count for one keyed bucket |
| `ResetAll()` | Routes, handlers, key functions, recorded calls, and counts |

Reset methods do not wait for active handlers. Use `WaitUntilIdle` first if a
handler may still be running.

## Waiting for active handlers

`InFlight` returns the number of handlers currently running. `WaitUntilIdle`
blocks until that number reaches zero or its context ends.

```go
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()

if err := server.WaitUntilIdle(ctx); err != nil {
	t.Fatalf("wait for mock server: %v", err)
}
```

`WaitUntilIdle` returns `nil` when the server becomes idle, or `ctx.Err()` if the
context ends first. It does not stop new requests. If no handler is running when
called, it returns immediately and does not wait for future requests.

## Response helpers

`ResponseWriter` provides helpers for common test responses:

```go
w.JSON(http.StatusOK, value)
w.HTML(http.StatusOK, "<p>ok</p>")
w.String(http.StatusOK, "ok")
w.Blob(http.StatusOK, "application/octet-stream", data)
w.NoContent(http.StatusNoContent)
```

For custom responses, use `Header`, `SetStatusCode`, `SetBodyJSON`, or
`SetBodyBytes`.

Set `ServerConfig.EnableLogging` to `true` to log incoming requests.

## Contributing

Found a bug, a confusing edge case, or a way to make HTTP tests nicer? Open an
issue or send a pull request. Small, focused changes are especially welcome.

Before submitting a pull request:

1. Add or update a test for the behavior you changed.
2. Run `go test ./...`.
3. Explain what changed and why.

Documentation fixes and new examples count too. If something made you stop and
scratch your head, improving it will probably help the next person.
