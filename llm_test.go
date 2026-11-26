package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

func TestCleanBody_StripsLinksImagesAndNonASCII(t *testing.T) {
	p := CloudMailInPayload{}
	p.HTML = `<html><body>` +
		`<p>Hi 📦 Ricardo,</p>` +
		`<img src="https://tracker.example.com/pixel.gif" alt="">` +
		`<p>Your package <a href="https://track.example.com/ABC123?utm=long">3781053936972</a> ` +
		`was delivered. Track at https://track.example.com/ABC123</p>` +
		`</body></html>`

	clean := cleanBody(p)

	assert.Contains(t, clean, "3781053936972")
	assert.Contains(t, clean, "was delivered")
	assert.NotContains(t, clean, "<")
	assert.NotContains(t, clean, "http")
	assert.NotContains(t, clean, "pixel.gif")
	assert.NotContains(t, clean, "📦")
	for _, r := range clean {
		assert.Less(t, r, rune(127), "non-ASCII rune %q left in body", r)
	}
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

type stubClassifier struct {
	info      *DeliveryInfo
	err       error
	healthErr error
}

func (s stubClassifier) Classify(_ context.Context, _ CloudMailInPayload) (*DeliveryInfo, error) {
	return s.info, s.err
}

func (s stubClassifier) Healthy(_ context.Context) error {
	return s.healthErr
}

func postPayload(t *testing.T, handler http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(samplePayload())
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func TestHandler_Delivery(t *testing.T) {
	handler := makeParseHandler(stubClassifier{info: &DeliveryInfo{
		IsDelivered:    true,
		Carrier:        "AliExpress",
		TrackingNumber: "3781053936972",
	}})

	rec := postPayload(t, handler)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp H
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "3781053936972", resp["tracking"])
	assert.Equal(t, "AliExpress", resp["provider"])
}

func TestHandler_UnknownProvider(t *testing.T) {
	handler := makeParseHandler(stubClassifier{info: &DeliveryInfo{
		IsDelivered:    true,
		TrackingNumber: "3781053936972",
	}})

	rec := postPayload(t, handler)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"provider":"Unknown"`)
}

func TestHandler_NotDelivery(t *testing.T) {
	handler := makeParseHandler(stubClassifier{info: &DeliveryInfo{IsDelivered: false}})

	rec := postPayload(t, handler)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandler_NoTrackingNumber(t *testing.T) {
	handler := makeParseHandler(stubClassifier{info: &DeliveryInfo{IsDelivered: true}})

	rec := postPayload(t, handler)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandler_ClassifierError(t *testing.T) {
	handler := makeParseHandler(stubClassifier{err: errors.New("LLM unreachable")})

	rec := postPayload(t, handler)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestHandler_BadJSON(t *testing.T) {
	handler := makeParseHandler(stubClassifier{})
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{not json"))
	rec := httptest.NewRecorder()
	handler(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

type spyClassifier struct {
	called bool
}

func (s *spyClassifier) Classify(_ context.Context, _ CloudMailInPayload) (*DeliveryInfo, error) {
	s.called = true
	return &DeliveryInfo{}, nil
}

func (s *spyClassifier) Healthy(_ context.Context) error {
	return nil
}

func TestHandler_EmptyPayloadRejected(t *testing.T) {
	spy := &spyClassifier{}
	handler := makeParseHandler(spy)

	req := httptest.NewRequest(http.MethodPost, "/parse", strings.NewReader("{}"))
	req.Header.Set("X-Forwarded-For", "203.0.113.10")
	rec := httptest.NewRecorder()
	handler(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.False(t, spy.called, "empty payload must not reach the LLM")
}

func TestHealthHandler_OK(t *testing.T) {
	handler := makeHealthHandler(stubClassifier{})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"status":"ok"`)
}

func TestHealthHandler_LLMUnavailable(t *testing.T) {
	handler := makeHealthHandler(stubClassifier{healthErr: errors.New("connection refused")})

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestHealthHandler_MethodNotAllowed(t *testing.T) {
	handler := makeHealthHandler(stubClassifier{})

	req := httptest.NewRequest(http.MethodPost, "/health", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestLLMClient_Healthy(t *testing.T) {
	var gotPath, gotMethod, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod, gotAuth = r.URL.Path, r.Method, r.Header.Get("Authorization")
		w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	t.Cleanup(server.Close)
	client := &LLMClient{
		apiURL:     server.URL + "/v1/chat/completions",
		model:      "test-model",
		apiKey:     "secret",
		httpClient: server.Client(),
	}

	require.NoError(t, client.Healthy(context.Background()))
	assert.Equal(t, "/v1/models", gotPath)
	assert.Equal(t, http.MethodGet, gotMethod)
	assert.Equal(t, "Bearer secret", gotAuth)
}

func TestLLMClient_HealthyUnavailable(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "loading model", http.StatusServiceUnavailable)
	}))

	require.Error(t, client.Healthy(context.Background()))
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	handler := makeParseHandler(stubClassifier{})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}
