package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
)

var version = "dev"

type H map[string]any

// CloudMailInPayload struct represents the JSON payload from CloudMailIn.
type CloudMailInPayload struct {
	Headers struct {
		From    string `json:"from"`
		Subject string `json:"subject"`
	} `json:"headers"`
	HTML  string `json:"html"`
	Plain string `json:"plain"`
}

// makeHandler handles incoming requests from CloudMailIn. Payloads are
// acknowledged immediately and classified in the background: LLM inference
// takes far longer than webhook senders are willing to wait.
func makeHandler(classifier Classifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Only POST method is accepted", http.StatusMethodNotAllowed)
			return
		}

		var payload CloudMailInPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "Failed to decode JSON payload", http.StatusBadRequest)
			return
		}

		log.Printf("Received payload: %s (%s)\n", payload.Headers.Subject, payload.Headers.From)
		go classify(classifier, payload)

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(H{"status": "accepted"})
	}
}

// classifySem bounds concurrent LLM calls; llama.cpp processes requests
// serially, so a burst of emails would otherwise pile up unbounded.
var classifySem = make(chan struct{}, 4)

// classify runs the LLM classification detached from the request context, so
// a caller disconnect does not abort it, and logs the verdict.
func classify(classifier Classifier, payload CloudMailInPayload) {
	classifySem <- struct{}{}
	defer func() { <-classifySem }()

	info, err := classifier.Classify(context.Background(), payload)
	if err != nil {
		log.Printf("Classification failed: %v", err)
		return
	}
	if !info.IsDelivered {
		log.Println("Not a delivery")
		return
	}
	if info.TrackingNumber == "" {
		log.Println("No tracking number found")
		return
	}
	provider := info.Carrier
	if provider == "" {
		provider = "Unknown"
	}
	log.Printf("Provider: %s, Tracking %s (evidence: %q):", provider, info.TrackingNumber, info.EvidencePhrase)
}

func main() {
	log.Printf("Version: %s", version)

	client := NewLLMClientFromEnv()
	log.Printf("Using LLM API at %s (model %s)", client.apiURL, client.model)

	http.HandleFunc("/", makeHandler(client))

	port := "8080"
	log.Printf("Server starting on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
