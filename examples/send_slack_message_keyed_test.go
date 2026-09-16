package examples_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httptest "github.com/nicandcranny/go-http-test"
	"github.com/nicandcranny/go-http-test/examples"
)

// This example shows KEYED mode: run parallel tests against the SAME routes,
// where each request gets a DIFFERENT response based on a per-request key.
//
// Compare with send_slack_message_test.go, which uses SIMPLE mode: one handler
// per route, shared by the whole (sequential) suite.
//
// Here the bucket key is the Slack "channel": each parallel subtest posts to its
// own channel and expects its own permalink back, all through one shared server
// and one shared pair of routes — with no cross-test interference.

const keyedSlackAddr = "127.0.0.1:0" // port 0 -> OS assigns a free port

// slackHTTP is a tiny direct HTTP helper so this example does not depend on the
// package-level singleton client (which is pinned to SLACK_URL).
type slackHTTP struct {
	baseURL string
	client  *http.Client
}

func (h slackHTTP) postMessage(t *testing.T, channel, text string) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"channel": channel, "text": text})
	req, _ := http.NewRequest(http.MethodPost, h.baseURL+examples.SlackChatPostMessagePath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := h.client.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	var out map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&out))
	return out
}

func (h slackHTTP) getPermalink(t *testing.T, channel, ts string) map[string]any {
	t.Helper()
	q := url.Values{}
	q.Add("channel", channel)
	q.Add("message_ts", ts)
	res, err := h.client.Get(h.baseURL + examples.SlackGetPermalinkPath + "?" + q.Encode())
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	var out map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&out))
	return out
}

func TestSlackKeyed_ParallelPerChannel(t *testing.T) {
	t.Parallel()

	server, err := httptest.NewServer(keyedSlackAddr, httptest.ServerConfig{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })

	// Declare each route ONCE, bucketed by the request's "channel".
	//   - postMessage reads channel from the JSON body
	//   - getPermalink reads channel from the query string
	server.RegisterKeyedRoute(http.MethodPost, examples.SlackChatPostMessagePath,
		func(r *httptest.Request) string {
			b, _ := io.ReadAll(r.Body) // body is buffered & restored for the handler
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			ch, _ := m["channel"].(string)
			return ch
		},
	)
	server.RegisterKeyedRoute(http.MethodGet, examples.SlackGetPermalinkPath,
		func(r *httptest.Request) string {
			return r.URL.Query().Get("channel")
		},
	)

	sh := slackHTTP{baseURL: "http://" + server.Addr(), client: &http.Client{Timeout: 2 * time.Second}}

	// Each channel is its own parallel test, its own key, its own response.
	channels := []struct {
		channel   string
		text      string
		ts        string
		permalink string
	}{
		{channel: "#alerts", text: "disk full", ts: "1111.0001", permalink: "https://x.slack.com/archives/ALERTS/p1"},
		{channel: "#deploys", text: "shipped v2", ts: "2222.0002", permalink: "https://x.slack.com/archives/DEPLOYS/p2"},
		{channel: "#random", text: "gm", ts: "3333.0003", permalink: "https://x.slack.com/archives/RANDOM/p3"},
	}

	for _, tc := range channels {
		t.Run(tc.channel, func(t *testing.T) {
			t.Parallel()

			// Register handlers scoped to THIS channel's key only. Other
			// channels' handlers on the same routes are untouched.
			server.RegisterKeyedHandler(http.MethodPost, examples.SlackChatPostMessagePath, tc.channel,
				func(w httptest.ResponseWriter, r *httptest.Request) {
					_ = w.JSON(200, map[string]any{
						"ok":      true,
						"channel": tc.channel,
						"ts":      tc.ts,
					})
				},
			)
			server.RegisterKeyedHandler(http.MethodGet, examples.SlackGetPermalinkPath, tc.channel,
				func(w httptest.ResponseWriter, r *httptest.Request) {
					_ = w.JSON(200, map[string]any{
						"ok":        true,
						"channel":   tc.channel,
						"permalink": tc.permalink,
					})
				},
			)

			// Exercise the two calls this test cares about.
			posted := sh.postMessage(t, tc.channel, tc.text)
			assert.Equal(t, tc.ts, posted["ts"])

			link := sh.getPermalink(t, tc.channel, tc.ts)
			assert.Equal(t, tc.permalink, link["permalink"])

			// Assertions read only THIS channel's bucket.
			assert.Equal(t, 1,
				server.GetNCallsByKey(http.MethodPost, examples.SlackChatPostMessagePath, tc.channel))
			assert.Equal(t, 1,
				server.GetNCallsByKey(http.MethodGet, examples.SlackGetPermalinkPath, tc.channel))

			postCalls := server.GetCallsByKey(http.MethodPost, examples.SlackChatPostMessagePath, tc.channel)
			require.Len(t, postCalls, 1)
			var sent map[string]any
			require.NoError(t, json.Unmarshal(postCalls[0].Body, &sent))
			assert.Equal(t, tc.text, sent["text"])
		})
	}
}
