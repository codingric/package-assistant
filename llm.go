package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/microcosm-cc/bluemonday"
)

const defaultLLMAPIURL = "http://llm-classifier:8080/v1/chat/completions"
const defaultLLMModel = "/models/qwen2.5-3b-instruct-q4_k_m.gguf"

// maxBodyChars bounds how much of the email body is sent to the model;
// long prompts dominate inference time on a small CPU-hosted model.
const maxBodyChars = 8000

const systemPrompt = `You classify inbound emails for a package tracking assistant.
Decide whether the email reports that a package was actually DELIVERED to the recipient, and extract the relevant details.

Rules:
- is_delivered is true only when the email says the package or order has been delivered (e.g. "has been delivered", "was delivered", "was left near the front door", "delivered today").
- Shipping confirmations, dispatch notices, customs clearance, "out for delivery", arrival at a facility or in the destination country, and estimated delivery dates are NOT deliveries: is_delivered is false.
- evidence_phrase is the short quote from the email that best supports your is_delivered decision, or "" if there is none.
- tracking_number is the shipment tracking number exactly as written (not an order id), or "" if none is present.
- carrier is the company delivering the package (e.g. "Amazon", "AliExpress", "Australia Post", "DHL"), taken from the sender or the content, or "" if unknown.

Respond with a JSON object only: {"is_delivered": boolean, "evidence_phrase": string, "carrier": string, "tracking_number": string}`

// deliveryResponseFormat constrains the model output via the llama.cpp
// json_schema response format.
var deliveryResponseFormat = responseFormat{
	Type: "json_schema",
	Schema: jsonSchema{
		Type: "object",
		Properties: map[string]any{
			"is_delivered":    map[string]string{"type": "boolean"},
			"evidence_phrase": map[string]string{"type": "string"},
			"carrier":         map[string]string{"type": "string"},
			"tracking_number": map[string]string{"type": "string"},
		},
		Required:             []string{"is_delivered", "evidence_phrase", "carrier", "tracking_number"},
		AdditionalProperties: false,
	},
}

// DeliveryInfo is the structured result extracted by the LLM.
type DeliveryInfo struct {
	IsDelivered    bool   `json:"is_delivered"`
	EvidencePhrase string `json:"evidence_phrase"`
	Carrier        string `json:"carrier"`
	TrackingNumber string `json:"tracking_number"`
}

// Classifier determines whether an email is a delivery notification and extracts its details.
type Classifier interface {
	Classify(ctx context.Context, payload CloudMailInPayload) (*DeliveryInfo, error)
	Healthy(ctx context.Context) error
}

// LLMClient classifies emails via an OpenAI-compatible chat completions endpoint (llama.cpp server).
type LLMClient struct {
	apiURL     string
	model      string
	apiKey     string
	httpClient *http.Client
}

// NewLLMClientFromEnv builds an LLMClient configured from the environment:
// LLM_API_URL, LLM_MODEL and LLM_API_KEY (all optional).
func NewLLMClientFromEnv() *LLMClient {
	return &LLMClient{
		apiURL:     getenv("LLM_API_URL", defaultLLMAPIURL),
		model:      getenv("LLM_MODEL", defaultLLMModel),
		apiKey:     os.Getenv("LLM_API_KEY"),
		httpClient: &http.Client{Timeout: 120 * time.Second},
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type jsonSchema struct {
	Type                 string         `json:"type"`
	Properties           map[string]any `json:"properties"`
	Required             []string       `json:"required"`
	AdditionalProperties bool           `json:"additionalProperties"`
}

type responseFormat struct {
	Type   string     `json:"type"`
	Schema jsonSchema `json:"schema"`
}

type chatCompletionRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	Temperature    float64        `json:"temperature"`
	MaxTokens      int            `json:"max_tokens"`
	ResponseFormat responseFormat `json:"response_format"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Classify sends the email to the LLM and parses its verdict.
func (c *LLMClient) Classify(ctx context.Context, payload CloudMailInPayload) (*DeliveryInfo, error) {
	user := fmt.Sprintf("From: %s\nSubject: %s\nBody:\n%s",
		payload.Headers.From, payload.Headers.Subject, cleanBody(payload))

	reqBody := chatCompletionRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: user},
		},
		Temperature:    0,
		MaxTokens:      300,
		ResponseFormat: deliveryResponseFormat,
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("encoding request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling LLM API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("reading LLM API response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LLM API returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var completion chatCompletionResponse
	if err := json.Unmarshal(body, &completion); err != nil {
		return nil, fmt.Errorf("decoding LLM API response: %w", err)
	}
	if completion.Error != nil {
		return nil, fmt.Errorf("LLM API error: %s", completion.Error.Message)
	}
	if len(completion.Choices) == 0 {
		return nil, fmt.Errorf("LLM API returned no choices")
	}

	return parseDeliveryInfo(completion.Choices[0].Message.Content)
}

// Healthy reports whether the LLM API is reachable and serving, by querying
// its models endpoint.
func (c *LLMClient) Healthy(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.modelsURL(), nil)
	if err != nil {
		return fmt.Errorf("building health request: %w", err)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("calling LLM API: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("LLM API returned %s", resp.Status)
	}
	return nil
}

// modelsURL derives the models endpoint from the configured chat completions URL.
func (c *LLMClient) modelsURL() string {
	u, err := url.Parse(c.apiURL)
	if err != nil {
		return c.apiURL
	}
	u.Path = "/v1/models"
	u.RawQuery = ""
	return u.String()
}

// parseDeliveryInfo unmarshals the model output, tolerating markdown code
// fences or surrounding prose.
func parseDeliveryInfo(content string) (*DeliveryInfo, error) {
	content = strings.TrimSpace(content)
	if i := strings.Index(content, "{"); i >= 0 {
		content = content[i:]
	}
	if i := strings.LastIndex(content, "}"); i >= 0 {
		content = content[:i+1]
	}

	var info DeliveryInfo
	if err := json.Unmarshal([]byte(content), &info); err != nil {
		return nil, fmt.Errorf("parsing LLM result: %w", err)
	}

	info.Carrier = strings.TrimSpace(info.Carrier)
	info.TrackingNumber = strings.TrimSpace(info.TrackingNumber)
	// The schema forces a string, so the model uses placeholders when no
	// tracking number exists; normalize them to empty.
	switch strings.ToLower(info.TrackingNumber) {
	case "", "not provided", "not available", "n/a", "none", "unknown":
		info.TrackingNumber = ""
	}
	return &info, nil
}

var whitespaceRegex = regexp.MustCompile(`\s+`)

// cleanBody strips HTML tags and collapses whitespace to keep the prompt small.
func cleanBody(payload CloudMailInPayload) string {
	text := payload.HTML
	if strings.TrimSpace(text) == "" {
		text = payload.Plain
	}
	sanitized := bluemonday.StrictPolicy().Sanitize(text)
	clean := strings.TrimSpace(whitespaceRegex.ReplaceAllString(sanitized, " "))
	if runes := []rune(clean); len(runes) > maxBodyChars {
		clean = string(runes[:maxBodyChars])
	}
	return clean
}
