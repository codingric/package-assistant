package main

import (
	"encoding/json"
	"log"
	"net/http"
)

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

// DeliveryInfo holds the extracted package information.
type DeliveryInfo struct {
	Carrier        string `json:"carrier"`
	TrackingNumber string `json:"tracking_number"`
}

// whitespaceRegex is used to collapse consecutive whitespace characters into a single space.

// httpHandler handles incoming requests from CloudMailIn.
func httpHandler(w http.ResponseWriter, r *http.Request) {
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

	p, err := NewPredictor(payload)
	if err != nil {
		log.Println("Not a delivery")
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	t, pr := p.ExtractTracking(), p.ExtarctProvider()
	if t == "" {
		log.Println("No tracking number found")
		http.Error(w, "No tracking number found", http.StatusBadRequest)
		return
	}
	if pr == "" {
		pr = "Unknown"
	}
	log.Printf("Provider: %s, Tracking %s:", pr, t)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(H{"tracking": t, "provider": pr})
}

func main() {

	http.HandleFunc("/", httpHandler)

	port := "8080"
	log.Printf("Server starting on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
