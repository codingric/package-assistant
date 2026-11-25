package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func chatResponse(content string) string {
	resp := map[string]any{
		"choices": []map[string]any{
			{"message": map[string]string{"role": "assistant", "content": content}},
		},
	}
	data, _ := json.Marshal(resp)
	return string(data)
}

func testClient(t *testing.T, handler http.Handler) *LLMClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &LLMClient{
		apiURL:     server.URL,
		model:      "test-model",
		httpClient: server.Client(),
	}
}

func samplePayload() CloudMailInPayload {
	var p CloudMailInPayload
	p.Headers.From = "AliExpress <notice@aliexpress.com>"
	p.Headers.Subject = "Your package has been delivered"
	p.HTML = `<html><body><p>Hi Ricardo,</p><p>Your package <b>3781053936972</b> has been successfully delivered!</p></body></html>`
	return p
}

func TestLLMClient_ClassifyDelivery(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(chatResponse(`{"is_delivered":true,"evidence_phrase":"has been successfully delivered","carrier":"AliExpress","tracking_number":"3781053936972"}`)))
	}))

	info, err := client.Classify(context.Background(), samplePayload())
	require.NoError(t, err)
	assert.True(t, info.IsDelivered)
	assert.Equal(t, "3781053936972", info.TrackingNumber)
	assert.Equal(t, "AliExpress", info.Carrier)
	assert.Equal(t, "has been successfully delivered", info.EvidencePhrase)
}

func TestLLMClient_ClassifyNotDelivery(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(chatResponse(`{"is_delivered":false,"evidence_phrase":"has arrived in your country/region","carrier":"AliExpress","tracking_number":"3781054117855"}`)))
	}))

	info, err := client.Classify(context.Background(), samplePayload())
	require.NoError(t, err)
	assert.False(t, info.IsDelivered)
}

func TestLLMClient_ClassifyNormalizesMissingTracking(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(chatResponse(`{"is_delivered":true,"evidence_phrase":"left near the front door","carrier":"Amazon.com","tracking_number":"Not provided"}`)))
	}))

	info, err := client.Classify(context.Background(), samplePayload())
	require.NoError(t, err)
	assert.True(t, info.IsDelivered)
	assert.Equal(t, "", info.TrackingNumber)
}

func TestLLMClient_ClassifyToleratesCodeFences(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(chatResponse("```json\n{\"is_delivered\":true,\"evidence_phrase\":\"delivered\",\"carrier\":\"Amazon.com\",\"tracking_number\":\"249-4398134-5142202\"}\n```")))
	}))

	info, err := client.Classify(context.Background(), samplePayload())
	require.NoError(t, err)
	assert.True(t, info.IsDelivered)
	assert.Equal(t, "249-4398134-5142202", info.TrackingNumber)
	assert.Equal(t, "Amazon.com", info.Carrier)
}

func TestLLMClient_RequestFormat(t *testing.T) {
	var gotAuth string
	var gotBody []byte
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(chatResponse(`{"is_delivered":true,"evidence_phrase":"x","carrier":"x","tracking_number":"1"}`)))
	}))
	client.apiKey = "secret"

	_, err := client.Classify(context.Background(), samplePayload())
	require.NoError(t, err)

	assert.Equal(t, "Bearer secret", gotAuth)

	var req chatCompletionRequest
	require.NoError(t, json.Unmarshal(gotBody, &req))
	assert.Equal(t, "test-model", req.Model)
	assert.Equal(t, "json_schema", req.ResponseFormat.Type)
	assert.Equal(t, "object", req.ResponseFormat.Schema.Type)
	assert.ElementsMatch(t,
		[]string{"is_delivered", "evidence_phrase", "carrier", "tracking_number"},
		req.ResponseFormat.Schema.Required)
	assert.Contains(t, req.ResponseFormat.Schema.Properties, "is_delivered")
	assert.False(t, req.ResponseFormat.Schema.AdditionalProperties)

	require.Len(t, req.Messages, 2)
	assert.Equal(t, "system", req.Messages[0].Role)
	assert.Equal(t, "user", req.Messages[1].Role)

	user := req.Messages[1].Content
	assert.Contains(t, user, "AliExpress <notice@aliexpress.com>")
	assert.Contains(t, user, "Your package has been delivered")
	assert.Contains(t, user, "3781053936972")
	assert.NotContains(t, user, "<b>", "HTML tags should be stripped from the body")
}

func TestLLMClient_ClassifyAPIError(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model overloaded", http.StatusServiceUnavailable)
	}))

	_, err := client.Classify(context.Background(), samplePayload())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "503")
}

func TestLLMClient_ClassifyInvalidJSON(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(chatResponse("I could not determine that.")))
	}))

	_, err := client.Classify(context.Background(), samplePayload())
	require.Error(t, err)
}

// syncBuffer is a goroutine-safe log sink for asserting on async classification output.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return buf
}

type stubClassifier struct {
	info *DeliveryInfo
	err  error
}

func (s stubClassifier) Classify(_ context.Context, _ CloudMailInPayload) (*DeliveryInfo, error) {
	return s.info, s.err
}

func postPayload(t *testing.T, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(samplePayload())
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func TestHandler_AcknowledgesImmediately(t *testing.T) {
	logs := captureLogs(t)
	handler := makeHandler(stubClassifier{info: &DeliveryInfo{
		IsDelivered:    true,
		Carrier:        "AliExpress",
		TrackingNumber: "3781053936972",
		EvidencePhrase: "has been successfully delivered",
	}})

	rec := postPayload(t, handler)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp H
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "accepted", resp["status"])

	assert.Eventually(t, func() bool {
		return strings.Contains(logs.String(), "Provider: AliExpress, Tracking 3781053936972")
	}, 2*time.Second, 10*time.Millisecond, "expected classification verdict in logs")
}

func TestHandler_UnknownProvider(t *testing.T) {
	logs := captureLogs(t)
	handler := makeHandler(stubClassifier{info: &DeliveryInfo{
		IsDelivered:    true,
		TrackingNumber: "3781053936972",
	}})

	rec := postPayload(t, handler)
	assert.Equal(t, http.StatusOK, rec.Code)

	assert.Eventually(t, func() bool {
		return strings.Contains(logs.String(), "Provider: Unknown")
	}, 2*time.Second, 10*time.Millisecond)
}

func TestHandler_NotDelivery(t *testing.T) {
	logs := captureLogs(t)
	handler := makeHandler(stubClassifier{info: &DeliveryInfo{IsDelivered: false}})

	rec := postPayload(t, handler)
	assert.Equal(t, http.StatusOK, rec.Code)

	assert.Eventually(t, func() bool {
		return strings.Contains(logs.String(), "Not a delivery")
	}, 2*time.Second, 10*time.Millisecond)
}

func TestHandler_NoTrackingNumber(t *testing.T) {
	logs := captureLogs(t)
	handler := makeHandler(stubClassifier{info: &DeliveryInfo{IsDelivered: true}})

	rec := postPayload(t, handler)
	assert.Equal(t, http.StatusOK, rec.Code)

	assert.Eventually(t, func() bool {
		return strings.Contains(logs.String(), "No tracking number found")
	}, 2*time.Second, 10*time.Millisecond)
}

func TestHandler_ClassifierError(t *testing.T) {
	logs := captureLogs(t)
	handler := makeHandler(stubClassifier{err: errors.New("LLM unreachable")})

	rec := postPayload(t, handler)
	assert.Equal(t, http.StatusOK, rec.Code)

	assert.Eventually(t, func() bool {
		return strings.Contains(logs.String(), "Classification failed: LLM unreachable")
	}, 2*time.Second, 10*time.Millisecond)
}

func TestHandler_BadJSON(t *testing.T) {
	handler := makeHandler(stubClassifier{})
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{not json"))
	rec := httptest.NewRecorder()
	handler(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	handler := makeHandler(stubClassifier{})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}
