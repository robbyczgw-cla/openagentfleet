// Package stt adapts a user-configured OpenAI-compatible transcription
// endpoint. Audio is streamed through the local daemon and is never persisted.
package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

type Config struct {
	Endpoint string
	APIKey   string
	Model    string
}

type Status struct {
	Available bool   `json:"available"`
	Detail    string `json:"detail,omitempty"`
}

type Client struct {
	endpoint string
	apiKey   string
	model    string
	http     *http.Client
}

func New(config Config) *Client {
	model := strings.TrimSpace(config.Model)
	if model == "" {
		model = "whisper-1"
	}
	return &Client{endpoint: strings.TrimSpace(config.Endpoint), apiKey: config.APIKey, model: model, http: &http.Client{Timeout: 90 * time.Second}}
}

func (c *Client) Status() Status {
	if c == nil || c.endpoint == "" {
		return Status{Detail: "Configure OPENAGENTFLEET_STT_URL for a local or OpenAI-compatible transcription endpoint."}
	}
	return Status{Available: true, Detail: "Speech-to-text is configured; audio is not retained by OpenAgentFleet."}
}

func (c *Client) Transcribe(ctx context.Context, filename, mediaType string, audio io.Reader) (string, error) {
	if c == nil || c.endpoint == "" {
		return "", errors.New("speech-to-text is not configured")
	}
	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, audio); err != nil {
		return "", err
	}
	if err := writer.WriteField("model", c.model); err != nil {
		return "", err
	}
	if mediaType != "" {
		_ = writer.WriteField("mime_type", mediaType)
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, &payload)
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if c.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return "", err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("transcription endpoint returned HTTP %d", response.StatusCode)
	}
	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", errors.New("transcription endpoint returned invalid JSON")
	}
	result.Text = strings.TrimSpace(result.Text)
	if result.Text == "" {
		return "", errors.New("transcription endpoint returned no text")
	}
	return result.Text, nil
}
