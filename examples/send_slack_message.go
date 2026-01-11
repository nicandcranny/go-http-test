package examples

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"

	"github.com/joho/godotenv"

	"github.com/nicandcranny/go-http-test/internal/httpclient"
)

func init() {
	// Load the .env file
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	slackClient = httpclient.New(os.Getenv("SLACK_URL"), http.DefaultClient)
}

const (
	SlackChatPostMessagePath = "/api/chat.postMessage"
	SlackGetPermalinkPath    = "/api/chat.getPermalink"
)

var (
	slackClient *httpclient.HttpClient
)

// SendSlackMessage sends message to Slack and return the url of the message.
func SendSlackMessage(ctx context.Context, channel, text string) (url_ string, err_ error) {
	reqBody := map[string]any{
		"channel": channel,
		"text":    text,
	}

	reqHeader := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + os.Getenv("SLACK_TOKEN"),
	}

	reqBodyByte, err := json.Marshal(&reqBody)
	if err != nil {
		return "", err
	}

	res, resBodyByte, err := slackClient.Do(
		ctx,
		http.MethodPost,
		SlackChatPostMessagePath,
		reqHeader,
		reqBodyByte,
		nil,
	)
	if err != nil {
		return "", fmt.Errorf("send slack message: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf(
			"send slack message: status code %d, res body: %s",
			res.StatusCode,
			string(resBodyByte),
		)
	}

	var resBody map[string]any
	if err := json.Unmarshal(resBodyByte, &resBody); err != nil {
		return "", fmt.Errorf("unmarshal res body: %w", err)
	}

	qParams := url.Values{}
	qParams.Add("channel", channel)
	qParams.Add("message_ts", resBody["ts"].(string))
	permalinkRes, permalinkResBodyByte, err := slackClient.Do(
		ctx,
		http.MethodGet,
		SlackGetPermalinkPath,
		reqHeader,
		nil,
		qParams,
	)
	if err != nil {
		return "", fmt.Errorf("get message permalink: %w", err)
	}
	if permalinkRes.StatusCode != http.StatusOK {
		return "", fmt.Errorf(
			"get message permalink: status code %d, res body: %s",
			permalinkRes.StatusCode,
			string(permalinkResBodyByte),
		)
	}

	var permalinkResBody map[string]any
	if err := json.Unmarshal(permalinkResBodyByte, &permalinkResBody); err != nil {
		return "", fmt.Errorf("unmarshal permalink res body: %w", err)
	}

	return permalinkResBody["permalink"].(string), nil
}
