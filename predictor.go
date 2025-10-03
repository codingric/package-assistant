package main

import (
	"log"
	"strings"

	"go.arsenm.dev/pcre"

	"github.com/microcosm-cc/bluemonday"
)

type Predictor struct {
	payload   CloudMailInPayload
	cleanText string
}

func NewPredictor(request CloudMailInPayload) (*Predictor, error) {
	b := bluemonday.StrictPolicy()
	sanitizedText := b.Sanitize(request.HTML)
	var whitespaceRegex = pcre.MustCompile(`\s+|\n+|\r+`)

	// Replace multiple whitespace characters (including newlines) with a single space.
	condensedText := whitespaceRegex.ReplaceAllString(sanitizedText, " ")
	// Trim any leading or trailing whitespace.
	cleanText := strings.TrimSpace(condensedText)

	log.Printf("Body: %s\n", cleanText)

	return &Predictor{
		payload:   request,
		cleanText: cleanText,
	}, nil

}

func (p *Predictor) IsDelivery() bool {
	deliveredRegex := pcre.MustCompile(`(?i)(?<=package|order|parcel).*(delivered|arrived)(?! in your country)`)
	return deliveredRegex.MatchString(p.cleanText)
}

func (p *Predictor) ExtractTracking() string {
	trackingRegex := pcre.MustCompile(`(?i)(package|number|order)[ :#]*(?P<id>[0-9][A-Z0-9-]{5,24})`)

	allMatches := trackingRegex.FindAllStringSubmatch(p.cleanText, -1)
	if allMatches == nil {
		return ""
	}

	// Get the index of the named group 'id'
	idIndex := trackingRegex.SubexpIndex("id")

	for _, match := range allMatches {
		if idIndex != -1 && len(match) > idIndex {
			return match[idIndex]
		}
	}

	return ""
}

func (p *Predictor) ExtarctProvider() string {
	parts := strings.Split(p.payload.Headers.From, " ")
	return strings.Join(parts[:len(parts)-1], " ")
}
