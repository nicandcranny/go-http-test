package httptest_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httptest "github.com/nicandcranny/go-http-test"
	"github.com/nicandcranny/go-http-test/internal/httpclient"
)

// These tests exercise the keyed API that makes the server usable from tests
// running in parallel against a single shared route. Each logical test uses a
// unique key (derived from the request) so its handler and its recorded calls
// are isolated from every other test hitting the same route.

const (
	// keyedAddress uses port 0 so the OS assigns a free port; each test's
	// server gets its own address (read back via server.Addr()), so the
	// top-level tests can run in parallel without colliding on a fixed port.
	keyedAddress = "127.0.0.1:0"
)

// newKeyedServer starts a server and registers a single POST /pay route whose
// bucket key is the "merchant_id" field of the JSON request body — mirroring how
// the-payment-service keys its mock partner responses by a per-test credential.
// It returns the server and a client already pointed at the server's address.
func newKeyedServer(t *testing.T) (*httptest.Server, *httpclient.HttpClient) {
	t.Helper()

	server, err := httptest.NewServer(keyedAddress, httptest.ServerConfig{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })

	server.RegisterKeyedRoute(http.MethodPost, "/pay", func(r *httptest.Request) string {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		if id, ok := m["merchant_id"].(string); ok {
			return id
		}
		return ""
	})

	return server, newTestClient("http://" + server.Addr())
}

func TestKeyed_ParallelIsolation(t *testing.T) {
	t.Parallel()

	server, client := newKeyedServer(t)

	// Each subtest uses its own merchant id -> its own bucket. They register
	// different responses and different expected call counts, yet share the
	// exact same route, and run in parallel.
	cases := []struct {
		name       string
		merchantID string
		nRequests  int
		status     int
	}{
		{name: "merchant_a", merchantID: "m-aaa", nRequests: 1, status: http.StatusOK},
		{name: "merchant_b", merchantID: "m-bbb", nRequests: 3, status: http.StatusCreated},
		{name: "merchant_c", merchantID: "m-ccc", nRequests: 5, status: http.StatusAccepted},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Register a handler scoped to this test's key only.
			server.RegisterKeyedHandler(http.MethodPost, "/pay", tc.merchantID,
				func(w httptest.ResponseWriter, r *httptest.Request) {
					_ = w.JSON(tc.status, map[string]string{"merchant_id": tc.merchantID})
				},
			)

			body := []byte(fmt.Sprintf(`{"merchant_id":%q}`, tc.merchantID))

			for i := 0; i < tc.nRequests; i++ {
				res, resBody, err := client.Do(ctx, http.MethodPost, "/pay",
					map[string]string{"Content-Type": "application/json"}, body, nil)
				require.NoError(t, err)
				assert.Equal(t, tc.status, res.StatusCode)
				assert.JSONEq(t, string(body), string(resBody))
			}

			// Assertions read only this test's bucket, so other parallel
			// subtests' calls never leak in.
			assert.Equal(t, tc.nRequests,
				server.GetNCallsByKey(http.MethodPost, "/pay", tc.merchantID))

			calls := server.GetCallsByKey(http.MethodPost, "/pay", tc.merchantID)
			require.Len(t, calls, tc.nRequests)
			for _, c := range calls {
				assert.JSONEq(t, string(body), string(c.Body))
			}
		})
	}
}

func TestKeyed_UnregisteredKeyReturns404(t *testing.T) {
	t.Parallel()

	server, client := newKeyedServer(t)

	// Route exists, but no handler is registered for this key.
	res, _, err := client.Do(ctx, http.MethodPost, "/pay",
		map[string]string{"Content-Type": "application/json"},
		[]byte(`{"merchant_id":"never-registered"}`), nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, res.StatusCode)

	// The call is still recorded in that key's bucket.
	assert.Equal(t, 1, server.GetNCallsByKey(http.MethodPost, "/pay", "never-registered"))
}

func TestKeyed_ResetCallsByKey_DoesNotTouchOtherKeys(t *testing.T) {
	t.Parallel()

	server, client := newKeyedServer(t)

	for _, id := range []string{"reset-a", "reset-b"} {
		server.RegisterKeyedHandler(http.MethodPost, "/pay", id,
			func(w httptest.ResponseWriter, r *httptest.Request) {
				w.SetStatusCode(http.StatusOK)
			})
		_, _, err := client.Do(ctx, http.MethodPost, "/pay",
			map[string]string{"Content-Type": "application/json"},
			[]byte(fmt.Sprintf(`{"merchant_id":%q}`, id)), nil)
		require.NoError(t, err)
	}

	assert.Equal(t, 1, server.GetNCallsByKey(http.MethodPost, "/pay", "reset-a"))
	assert.Equal(t, 1, server.GetNCallsByKey(http.MethodPost, "/pay", "reset-b"))

	// Resetting one bucket must not affect the other.
	server.ResetCallsByKey(http.MethodPost, "/pay", "reset-a")

	assert.Equal(t, 0, server.GetNCallsByKey(http.MethodPost, "/pay", "reset-a"))
	assert.Empty(t, server.GetCallsByKey(http.MethodPost, "/pay", "reset-a"))
	assert.Equal(t, 1, server.GetNCallsByKey(http.MethodPost, "/pay", "reset-b"))
}

// TestKeyed_ConcurrentRegistrationAndRequests hammers the server with concurrent
// registrations and requests across many keys to catch data races
// (run with -race).
func TestKeyed_ConcurrentRegistrationAndRequests(t *testing.T) {
	t.Parallel()

	server, client := newKeyedServer(t)

	const nKeys = 25
	var wg sync.WaitGroup
	for i := 0; i < nKeys; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("race-%d", i)

			server.RegisterKeyedHandler(http.MethodPost, "/pay", id,
				func(w httptest.ResponseWriter, r *httptest.Request) {
					w.SetStatusCode(http.StatusOK)
				})

			_, _, err := client.Do(ctx, http.MethodPost, "/pay",
				map[string]string{"Content-Type": "application/json"},
				[]byte(fmt.Sprintf(`{"merchant_id":%q}`, id)), nil)
			assert.NoError(t, err)
		}(i)
	}
	wg.Wait()

	for i := 0; i < nKeys; i++ {
		id := fmt.Sprintf("race-%d", i)
		assert.Equal(t, 1, server.GetNCallsByKey(http.MethodPost, "/pay", id))
	}
}
