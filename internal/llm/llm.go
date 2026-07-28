// Package llm is Trivial's model-provider abstraction (SPEC.md §5).
//
// The interface is deliberately small — one structured-output completion call —
// so swapping providers means writing one file.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Provider names accepted by New, for help text and validation.
const (
	ProviderGemini = "gemini"
	ProviderVertex = "vertex"
	ProviderMock   = "mock"
)

// Task identifies which stage of the pipeline a request comes from. Real
// providers ignore it; the mock provider uses it to shape its canned replies.
type Task string

const (
	TaskResearch Task = "research"
	TaskRender   Task = "render"
	TaskGrade    Task = "grade"
	TaskExplain  Task = "explain"
)

// Request is one completion call.
type Request struct {
	Task        Task
	System      string
	Prompt      string
	Schema      *Schema // when set, the reply must be JSON matching this shape
	Temperature float64
	MaxTokens   int

	// Vars carries the request's semantic inputs (subject, canonical answer,
	// the user's response, …) alongside the rendered Prompt that already
	// contains them. Network providers ignore Vars entirely; it exists so the
	// offline mock provider can respond meaningfully without parsing prose.
	Vars map[string]string
}

// Var reads a request variable, returning "" when absent.
func (r Request) Var(name string) string { return r.Vars[name] }

// Client is a text-in, text-out model provider.
type Client interface {
	// Complete returns the model's reply, which is JSON when Request.Schema is set.
	Complete(ctx context.Context, req Request) (string, error)
	// Name identifies the provider and model, for logs and diagnostics.
	Name() string
}

// DefaultModel is the model used when none is configured. Generation is bursty
// and grading is on the critical path of every answer, so the default is a
// flash-tier model rather than a pro one.
const DefaultModel = "gemini-3.5-flash"

var (
	// ErrNoAPIKey is returned when a provider needs an API key it does not have.
	ErrNoAPIKey = errors.New("no LLM API key configured")
	// ErrNoCredentials is returned when Application Default Credentials cannot
	// be resolved for a provider that authenticates with them.
	ErrNoCredentials = errors.New("no Google credentials found")
)

// Options configure provider construction.
type Options struct {
	Provider string
	Model    string
	Endpoint string

	// APIKey authenticates the AI Studio provider.
	APIKey string

	// Project and Location address the Vertex AI provider. An empty Project
	// falls back to the one named by the credentials; an empty Location to
	// DefaultVertexLocation.
	Project  string
	Location string
	// CredentialsFile is a path to a service account JSON key for Vertex AI.
	// Empty falls back to Application Default Credentials.
	CredentialsFile string
}

// New builds the client named by opts.Provider.
//
// Vertex AI is the default and the only provider Trivial talks to in anger.
// The others exist for narrower reasons: "gemini" is the same models reached
// with a plain API key instead of a service account, and "mock" is the offline
// stand-in used by the tests. The interface is what a future provider —
// selectable per topic — would plug into.
//
// ctx must outlive the returned client: providers that refresh credentials
// capture it for the lifetime of the process.
func New(ctx context.Context, opts Options) (Client, error) {
	switch strings.ToLower(strings.TrimSpace(opts.Provider)) {
	case "", ProviderVertex, "vertexai", "vertex-ai":
		return NewVertex(ctx, opts)

	case ProviderGemini, "google", "aistudio":
		if opts.APIKey == "" {
			return nil, fmt.Errorf(
				"%w: the gemini provider needs TRIVIAL_LLM_API_KEY; for a service account use the default vertex provider",
				ErrNoAPIKey)
		}
		return NewGemini(opts), nil

	case ProviderMock:
		return NewMock(), nil

	default:
		return nil, fmt.Errorf(
			"unknown LLM provider %q (want \"vertex\", \"gemini\" or \"mock\")", opts.Provider)
	}
}

// Schema describes the JSON shape a structured response must take. It is the
// OpenAPI subset Gemini's responseSchema accepts, which is also close enough to
// JSON Schema to validate against locally.
type Schema struct {
	Type        Type               `json:"type"`
	Description string             `json:"description,omitempty"`
	Properties  map[string]*Schema `json:"properties,omitempty"`
	// PropertyOrdering pins field order in the model's output, which measurably
	// improves adherence for object schemas.
	PropertyOrdering []string `json:"propertyOrdering,omitempty"`
	Required         []string `json:"required,omitempty"`
	Items            *Schema  `json:"items,omitempty"`
	Enum             []string `json:"enum,omitempty"`
	Minimum          *float64 `json:"minimum,omitempty"`
	Maximum          *float64 `json:"maximum,omitempty"`
}

// Type is a JSON schema type name as the provider spells it.
type Type string

const (
	TypeString  Type = "STRING"
	TypeInteger Type = "INTEGER"
	TypeNumber  Type = "NUMBER"
	TypeBoolean Type = "BOOLEAN"
	TypeArray   Type = "ARRAY"
	TypeObject  Type = "OBJECT"
)

// Num is a helper for the pointer-valued Minimum/Maximum fields.
func Num(v float64) *float64 { return &v }

// Probe sends a minimal request to check that a provider and model actually
// work together — the difference between a model ID being listed and being
// usable from your project.
//
// The token ceiling is generous rather than minimal: reasoning models spend
// output tokens before emitting text, so a tight budget makes a perfectly good
// model look broken. A ceiling costs nothing unless it is used.
func Probe(ctx context.Context, c Client) error {
	_, err := c.Complete(ctx, Request{
		Task:      TaskGrade, // the cheapest task shape; only the reply matters
		Prompt:    "Reply with the single word: ok",
		MaxTokens: ReasoningHeadroom,
	})
	return err
}

// ReasoningHeadroom is the smallest output ceiling that reliably leaves room
// for a reasoning model to think and still answer. Use it as the floor for any
// request, however short the expected reply.
const ReasoningHeadroom = 4096

// DecodeJSON parses a structured response into v, tolerating the ```json
// fences some providers add even when asked for raw JSON.
func DecodeJSON(text string, v any) error {
	s := strings.TrimSpace(text)
	if strings.HasPrefix(s, "```") {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	return json.Unmarshal([]byte(strings.TrimSpace(s)), v)
}
