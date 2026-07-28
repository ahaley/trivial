package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// DefaultVertexLocation is the region Vertex requests are sent to.
//
// "global" rather than a named region: Gemini models reach the global endpoint
// first and most reliably, and a model missing from one region is the usual
// cause of a 404 here.
const DefaultVertexLocation = "global"

// vertexScope is the OAuth2 scope a service account needs to call Vertex AI.
const vertexScope = "https://www.googleapis.com/auth/cloud-platform"

// NewVertex builds a client for Vertex AI, authenticated with Google
// Application Default Credentials.
//
// Credentials are resolved in the usual ADC order: the service-account JSON at
// GOOGLE_APPLICATION_CREDENTIALS, then gcloud's application-default login, then
// the attached identity of a GCE/Cloud Run/GKE workload. The returned client
// refreshes its own access tokens, so it stays valid past their hour lifetime.
//
// ctx must outlive the client: it is captured for those refreshes, so passing a
// per-request context would break the first refresh after it is cancelled.
func NewVertex(ctx context.Context, opts Options) (Client, error) {
	location := opts.Location
	if location == "" {
		location = DefaultVertexLocation
	}
	model := opts.Model
	if model == "" {
		model = DefaultModel
	}

	creds, err := vertexCredentials(ctx, opts.CredentialsFile)
	if err != nil {
		return nil, err
	}

	// Fall back to the project the credentials themselves name, so a service
	// account key usually needs no project setting at all.
	project := opts.Project
	if project == "" {
		project = creds.ProjectID
	}
	if project == "" {
		return nil, errors.New(
			"vertex: no project ID — set TRIVIAL_VERTEX_PROJECT, or use credentials that name one")
	}

	// oauth2's client attaches and refreshes the bearer token itself.
	httpClient := oauth2.NewClient(ctx, creds.TokenSource)
	httpClient.Timeout = 3 * time.Minute

	return &googleClient{
		name:         fmt.Sprintf("vertex/%s (%s, %s)", model, project, location),
		url:          VertexURL(opts.Endpoint, project, location, model),
		http:         httpClient,
		notFoundHint: notFoundHint(location),
		// No API key: the transport carries the credentials.
	}, nil
}

// notFoundHint turns a 404 into the next thing to try. A model missing from a
// named region is usually present at the global endpoint, and that is a far
// more common cause than a genuinely wrong model ID.
func notFoundHint(location string) string {
	if location == "global" {
		return "run `trivial models` to see what this project offers"
	}
	return fmt.Sprintf(
		"this is region %q; most Gemini models are served from the global endpoint, "+
			"so try --vertex-location global (or clear TRIVIAL_VERTEX_LOCATION), "+
			"then `trivial models` to confirm", location)
}

// adcEnvVar is the process-wide variable the Google libraries read a
// credentials path from.
const adcEnvVar = "GOOGLE_APPLICATION_CREDENTIALS"

// vertexCredentials loads the service account named by path, falling back to
// Application Default Credentials when no path is given.
//
// An explicit path is applied by setting GOOGLE_APPLICATION_CREDENTIALS for
// this process and then going through the normal ADC lookup. Parsing the file
// directly would mean google.CredentialsFromJSON, which is deprecated for
// skipping validation that the ADC path performs; borrowing the environment
// variable keeps that validation and needs no deprecated API.
//
// The write is confined to Trivial's own process, so it cannot disturb whatever
// the surrounding shell has GOOGLE_APPLICATION_CREDENTIALS set to.
func vertexCredentials(ctx context.Context, path string) (*google.Credentials, error) {
	if path != "" {
		// Check the file up front: ADC's own error for a bad path is generic,
		// and a misconfigured credentials path is the likeliest failure here.
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrNoCredentials, err)
		}
		if err := os.Setenv(adcEnvVar, path); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrNoCredentials, err)
		}
	}

	creds, err := google.FindDefaultCredentials(ctx, vertexScope)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoCredentials, err)
	}
	return creds, nil
}

// VertexModel is a publisher model as Vertex reports it.
type VertexModel struct {
	// ID is the bare model ID, which is what --llm-model takes.
	ID string
	// VersionID is the specific version behind the ID, when reported.
	VersionID string
	// LaunchStage is GA, PUBLIC_PREVIEW, and so on.
	LaunchStage string
}

// ListVertexModels asks a project which Google publisher models it can reach in
// a region — the authoritative answer to "what do I put in --llm-model".
//
// Not every project has permission to list, so callers should be ready to fall
// back to probing candidate IDs directly.
func ListVertexModels(ctx context.Context, opts Options) ([]VertexModel, error) {
	location := opts.Location
	if location == "" {
		location = DefaultVertexLocation
	}
	creds, err := vertexCredentials(ctx, opts.CredentialsFile)
	if err != nil {
		return nil, err
	}

	client := oauth2.NewClient(ctx, creds.TokenSource)
	client.Timeout = 60 * time.Second

	host := "aiplatform.googleapis.com"
	if location != "global" {
		host = location + "-" + host
	}
	// The publisher model list lives under v1beta1, not the v1 used for
	// generateContent.
	url := fmt.Sprintf("https://%s/v1beta1/publishers/google/models?pageSize=200", host)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list models: %s: %s", resp.Status, snippet(raw))
	}

	var parsed struct {
		PublisherModels []struct {
			Name        string `json:"name"`
			VersionID   string `json:"versionId"`
			LaunchStage string `json:"launchStage"`
		} `json:"publisherModels"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("list models: decoding response: %w", err)
	}

	out := make([]VertexModel, 0, len(parsed.PublisherModels))
	for _, m := range parsed.PublisherModels {
		// Names come back as "publishers/google/models/<id>".
		id := m.Name
		if i := strings.LastIndex(id, "/"); i >= 0 {
			id = id[i+1:]
		}
		if id == "" {
			continue
		}
		out = append(out, VertexModel{ID: id, VersionID: m.VersionID, LaunchStage: m.LaunchStage})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// VertexURL builds the generateContent endpoint for a project, location and
// model. An explicit endpoint overrides the regional host.
//
// The "global" location is served by the unprefixed host; every region has its
// own, and sending a request to the wrong one fails rather than redirecting.
func VertexURL(endpoint, project, location, model string) string {
	base := strings.TrimRight(endpoint, "/")
	if base == "" {
		host := "aiplatform.googleapis.com"
		if location != "global" {
			host = location + "-" + host
		}
		base = "https://" + host + "/v1"
	}
	return fmt.Sprintf("%s/projects/%s/locations/%s/publishers/google/models/%s:generateContent",
		base, project, location, model)
}
