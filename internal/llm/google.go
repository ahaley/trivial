package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// googleClient speaks the Gemini generateContent protocol.
//
// AI Studio and Vertex AI accept the same request and response bodies; they
// differ only in the URL they are reached at and in how the call is
// authenticated. Both are built on this one client — see gemini.go and
// vertex.go for the two constructors.
type googleClient struct {
	name string
	// url is the fully-formed generateContent endpoint, model included.
	url string
	// http performs the call. For Vertex it already carries the credentials.
	http *http.Client
	// apiKey is sent as x-goog-api-key. Empty when http handles auth.
	apiKey string
	// userProject is sent as x-goog-user-project, the project API usage is
	// billed to when the URL does not already name one. Only ever a quota
	// project the credentials themselves name: sending a project the caller
	// lacks serviceusage.services.use on turns a working call into a 403.
	userProject string
	// notFoundHint is appended to 404s. A missing model is nearly always a
	// region problem rather than a bad ID, and the provider knows which region
	// it was pointed at while the caller does not.
	notFoundHint string
}

// Name reports the provider and model.
func (g *googleClient) Name() string { return g.name }

type geminiPart struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiGenConfig struct {
	Temperature      *float64 `json:"temperature,omitempty"`
	MaxOutputTokens  int      `json:"maxOutputTokens,omitempty"`
	ResponseMIMEType string   `json:"responseMimeType,omitempty"`
	ResponseSchema   *Schema  `json:"responseSchema,omitempty"`
}

type geminiRequest struct {
	SystemInstruction *geminiContent   `json:"systemInstruction,omitempty"`
	Contents          []geminiContent  `json:"contents"`
	GenerationConfig  *geminiGenConfig `json:"generationConfig,omitempty"`
}

type geminiResponse struct {
	Candidates []struct {
		Content      geminiContent `json:"content"`
		FinishReason string        `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// Complete sends one request, retrying transient failures with backoff.
func (g *googleClient) Complete(ctx context.Context, req Request) (string, error) {
	body := geminiRequest{
		Contents: []geminiContent{{Role: "user", Parts: []geminiPart{{Text: req.Prompt}}}},
	}
	if req.System != "" {
		body.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: req.System}}}
	}
	cfg := &geminiGenConfig{MaxOutputTokens: req.MaxTokens}
	if req.Temperature > 0 {
		cfg.Temperature = &req.Temperature
	}
	if req.Schema != nil {
		cfg.ResponseMIMEType = "application/json"
		cfg.ResponseSchema = req.Schema
	}
	body.GenerationConfig = cfg

	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("encode request: %w", err)
	}

	const attempts = 3
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			// Exponential backoff: 1s, 2s.
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(1<<(attempt-2)) * time.Second):
			}
		}
		text, retryable, err := g.once(ctx, payload)
		if err == nil {
			return text, nil
		}
		lastErr = err
		if !retryable {
			return "", err
		}
	}
	return "", fmt.Errorf("%s request failed after %d attempts: %w", g.name, attempts, lastErr)
}

// once performs a single HTTP round trip. It reports whether the failure is
// worth retrying.
func (g *googleClient) once(ctx context.Context, payload []byte) (text string, retryable bool, err error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, g.url, bytes.NewReader(payload))
	if err != nil {
		return "", false, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if g.apiKey != "" {
		httpReq.Header.Set("x-goog-api-key", g.apiKey)
	}
	if g.userProject != "" {
		httpReq.Header.Set(userProjectHeader, g.userProject)
	}

	resp, err := g.http.Do(httpReq)
	if err != nil {
		// Network-level failures are usually transient. Credential refresh
		// failures also surface here and are worth one more try.
		return "", true, fmt.Errorf("call %s: %w", g.name, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", true, fmt.Errorf("read %s response: %w", g.name, err)
	}

	if resp.StatusCode != http.StatusOK {
		retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		if resp.StatusCode == http.StatusForbidden && mentionsQuotaProject(raw) {
			return "", false, fmt.Errorf("%s: %s (%s)", g.name, quotaProjectHint, snippet(raw))
		}
		if resp.StatusCode == http.StatusNotFound {
			hint := g.notFoundHint
			if hint == "" {
				hint = "run `trivial models` to see what this project offers"
			}
			return "", false, fmt.Errorf("%s: model not available — %s (%s)",
				g.name, hint, snippet(raw))
		}
		return "", retry, fmt.Errorf("%s returned %s: %s", g.name, resp.Status, snippet(raw))
	}

	var parsed geminiResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", false, fmt.Errorf("decode %s response: %w", g.name, err)
	}
	if parsed.Error != nil {
		retry := parsed.Error.Code == 429 || parsed.Error.Code >= 500
		return "", retry, fmt.Errorf("%s error %d: %s", g.name, parsed.Error.Code, parsed.Error.Message)
	}
	if parsed.PromptFeedback.BlockReason != "" {
		return "", false, fmt.Errorf("%s blocked the prompt: %s", g.name, parsed.PromptFeedback.BlockReason)
	}
	if len(parsed.Candidates) == 0 {
		return "", true, fmt.Errorf("%s returned no candidates", g.name)
	}

	cand := parsed.Candidates[0]
	var sb strings.Builder
	for _, p := range cand.Content.Parts {
		sb.WriteString(p.Text)
	}
	out := strings.TrimSpace(sb.String())
	if out == "" {
		if cand.FinishReason == "MAX_TOKENS" {
			// Reasoning models spend output tokens thinking before they emit
			// any text, so too small a budget yields a well-formed response
			// with nothing in it. Retrying cannot help — only a bigger ceiling.
			return "", false, fmt.Errorf(
				"%s: the token budget ran out before any text was produced, which usually "+
					"means the model spent it reasoning — raise MaxTokens for this request", g.name)
		}
		return "", false, fmt.Errorf("%s returned empty text (finish reason %q)", g.name, cand.FinishReason)
	}
	return out, false, nil
}

// userProjectHeader names the project API usage is billed to.
const userProjectHeader = "x-goog-user-project"

// quotaProjectHint is the fix for the one 403 that is not an IAM problem. It
// covers both halves of it: a user login with no project to bill API usage to,
// and one naming a quota project the caller may not use.
const quotaProjectHint = "this credential needs a usable quota project — set one with " +
	"`gcloud auth application-default set-quota-project <project>`"

// mentionsQuotaProject reports whether a 403 is the missing-quota-project one
// rather than a genuine permission failure.
func mentionsQuotaProject(body []byte) bool {
	s := strings.ToLower(string(body))
	return strings.Contains(s, "quota project") || strings.Contains(s, "user project")
}

// snippet trims an error body to something loggable.
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}
