package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
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

// makeParseHandler handles incoming mail payloads from CloudMailIn. It
// responds only after the LLM has classified the email, so callers must
// tolerate the inference latency.
func makeParseHandler(classifier Classifier) http.HandlerFunc {
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

		if payload.Headers.From == "" && payload.Headers.Subject == "" && payload.HTML == "" && payload.Plain == "" {
			log.Printf("Rejecting empty payload from %s, UA %q", clientIP(r), r.UserAgent())
			http.Error(w, "Empty payload", http.StatusBadRequest)
			return
		}

		log.Printf("Received payload: %s (%s) from %s\n", payload.Headers.Subject, payload.Headers.From, clientIP(r))

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

// makeHealthHandler reports whether the LLM backend is available, suitable
// for use as a readiness probe.
func makeHealthHandler(classifier Classifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Only GET method is accepted", http.StatusMethodNotAllowed)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err := classifier.Healthy(ctx); err != nil {
			log.Printf("Health check failed: %v", err)
			http.Error(w, "LLM unavailable", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(H{"status": "ok"})
	}
}

// clientIP returns the originating client IP, honoring X-Forwarded-For when
// the service sits behind an ingress.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.Index(xff, ","); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return xff
	}
	return r.RemoteAddr
}

func main() {
	log.Printf("Version: %s", version)

	client := NewLLMClientFromEnv()
	log.Printf("Using LLM API at %s (model %s)", client.apiURL, client.model)

	mux := http.NewServeMux()
	mux.HandleFunc("/parse", makeParseHandler(client))
	mux.HandleFunc("/health", makeHealthHandler(client))

	port := "8080"
	log.Printf("Server starting on port %s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
