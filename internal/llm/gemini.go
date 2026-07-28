package llm

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// DefaultGeminiEndpoint is the AI Studio base URL.
const DefaultGeminiEndpoint = "https://generativelanguage.googleapis.com/v1beta"

// NewGemini builds a client for Google AI Studio, authenticated with a plain
// API key. For a service account, use NewVertex instead.
func NewGemini(opts Options) Client {
	endpoint := strings.TrimRight(opts.Endpoint, "/")
	if endpoint == "" {
		endpoint = DefaultGeminiEndpoint
	}
	model := opts.Model
	if model == "" {
		model = DefaultModel
	}
	return &googleClient{
		name:   "gemini/" + model,
		url:    fmt.Sprintf("%s/models/%s:generateContent", endpoint, model),
		apiKey: opts.APIKey,
		// Generation prompts ask for dozens of facts at once, so allow a
		// generous per-call ceiling.
		http: &http.Client{Timeout: 3 * time.Minute},
	}
}
