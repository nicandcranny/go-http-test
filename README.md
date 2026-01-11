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

## Installation

To use go-http-test in your Go projects, you need to have Go (>=1.23) installed and set up. Then, you can install the library using `go get`:

```bash
go get github.com/nicandcranny/go-http-test
```

## Example Usage

To see real-life usage example, you can check it out in the [/examples](/examples).

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

## Contributing

go-http-test is an open source project, and we welcome contributions from the community. If you find a bug, have an enhancement in mind, or want to propose a new feature, please open an issue or submit a pull request on the GitHub repository.

Happy testing with go-http-test! If you have any questions or need further assistance, feel free to reach out to the project maintainers.
