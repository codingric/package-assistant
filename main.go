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

// makeHandler handles incoming requests from CloudMailIn. It responds only
// after the LLM has classified the email, so callers must tolerate the
// inference latency.
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

		// Detached from the request context: a caller disconnect must not
		// abort an in-flight classification on the slow LLM.
		info, err := classifier.Classify(context.Background(), payload)
		if err != nil {
			log.Printf("Classification failed: %v", err)
			http.Error(w, "Classification failed", http.StatusBadGateway)
			return
		}
		if !info.IsDelivered {
			log.Println("Not a delivery")
			http.Error(w, "Not a delivery", http.StatusBadRequest)
			return
		}
		if info.TrackingNumber == "" {
			log.Println("No tracking number found")
			http.Error(w, "No tracking number found", http.StatusBadRequest)
			return
		}
		provider := info.Carrier
		if provider == "" {
			provider = "Unknown"
		}
		log.Printf("Provider: %s, Tracking %s (evidence: %q):", provider, info.TrackingNumber, info.EvidencePhrase)

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(H{"tracking": info.TrackingNumber, "provider": provider})
	}
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
