package examples_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	httptest "github.com/nicandcranny/go-http-test"
	"github.com/nicandcranny/go-http-test/examples"

	"github.com/stretchr/testify/suite"
)

var (
	ctx = context.Background()
)

type slackTestSuite struct {
	suite.Suite
	mockSlackServer *httptest.Server
}

func TestSlackTestSuite(t *testing.T) {
	baseURL := strings.ReplaceAll(os.Getenv("SLACK_URL"), "http://", "")
	mockSlackServer, err := httptest.NewServer(baseURL, httptest.ServerConfig{})
	if err != nil {
		panic(err)
	}
	suite.Run(t, &slackTestSuite{
		mockSlackServer: mockSlackServer,
	})
}

func (s *slackTestSuite) BeforeTest(_, _ string) {
	s.mockSlackServer.ResetNCalls() // Or s.mockSlackServer.ResetAll() to clear the handlers too.
}

func (s *slackTestSuite) TestSendSlackMessage_Success() {
	channel := "#channelnotchanel"
	text := "textimony"
	mockTs := "1503435956.000247"
	mockURL := "https://ghostbusters.slack.com/archives/C1H9RESGL/p135854651700023?thread_ts=1358546515.000008&cid=C1H9RESGL"

	s.mockSlackServer.RegisterHandler(
		http.MethodPost,
		examples.SlackChatPostMessagePath,
		func(w httptest.ResponseWriter, r *httptest.Request) {
			// Assert the request header & body.
			s.Equal("Bearer "+os.Getenv("SLACK_TOKEN"), r.Header.Get("Authorization"))

			reqBodyByte, err := io.ReadAll(r.Body)
			s.NoError(err)
			var reqBody map[string]string
			s.NoError(json.Unmarshal(reqBodyByte, &reqBody))
			s.Equal(channel, reqBody["channel"])
			s.Equal(text, reqBody["text"])

			w.SetStatusCode(http.StatusOK)
			w.SetBodyJSON(map[string]any{
				"ok":      true,
				"channel": channel,
				"ts":      mockTs,
				"message": map[string]any{
					"text":     text,
					"username": "ecto1",
					"bot_id":   "B123ABC456",
					"type":     "message",
					"subtype":  "bot_message",
					"ts":       "1503435956.000247",
				},
			})
		},
	)

	s.mockSlackServer.RegisterHandler(
		http.MethodGet,
		examples.SlackGetPermalinkPath,
		func(w httptest.ResponseWriter, r *httptest.Request) {
			// Assert the request header & param.
			s.Equal("Bearer "+os.Getenv("SLACK_TOKEN"), r.Header.Get("Authorization"))
			s.Equal(channel, r.FormValue("channel"))

			w.SetStatusCode(http.StatusOK)
			w.SetBodyJSON(map[string]any{
				"ok":        true,
				"channel":   channel,
				"permalink": mockURL,
			})
		},
	)

	url, err := examples.SendSlackMessage(ctx, channel, text)
	s.NoError(err)
	s.Equal(mockURL, url)

	s.Equal(1, s.mockSlackServer.GetNCalls(http.MethodPost, examples.SlackChatPostMessagePath))
	s.Equal(1, s.mockSlackServer.GetNCalls(http.MethodGet, examples.SlackGetPermalinkPath))
}

func (s *slackTestSuite) TestSendSlackMessage_Failed() {
	channel := "#channelnotchanel"
	text := "textimony"

	s.mockSlackServer.RegisterHandler(
		http.MethodPost,
		examples.SlackChatPostMessagePath,
		func(w httptest.ResponseWriter, r *httptest.Request) {
			// Assert the request header & body.
			s.Equal("Bearer "+os.Getenv("SLACK_TOKEN"), r.Header.Get("Authorization"))

			reqBodyByte, err := io.ReadAll(r.Body)
			s.NoError(err)
			var reqBody map[string]string
			s.NoError(json.Unmarshal(reqBodyByte, &reqBody))
			s.Equal(channel, reqBody["channel"])
			s.Equal(text, reqBody["text"])

			w.SetStatusCode(http.StatusNotFound)
			w.SetBodyJSON(map[string]any{
				"ok":    true,
				"error": "channel_not_found",
			})
		},
	)

	url, err := examples.SendSlackMessage(ctx, channel, text)
	s.Error(err)
	s.Empty(url)

	s.Equal(1, s.mockSlackServer.GetNCalls(http.MethodPost, examples.SlackChatPostMessagePath))
	s.Equal(0, s.mockSlackServer.GetNCalls(http.MethodGet, examples.SlackGetPermalinkPath))
}
