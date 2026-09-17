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

	auth, err := ResolveVertexAuth(ctx, opts)
	if err != nil {
		return nil, err
	}

	// oauth2's client attaches and refreshes the bearer token itself.
	httpClient := oauth2.NewClient(ctx, auth.TokenSource)
	httpClient.Timeout = 3 * time.Minute

	return &googleClient{
		name:         fmt.Sprintf("vertex/%s (%s, %s)", model, auth.Project, location),
		url:          VertexURL(opts.Endpoint, auth.Project, location, model),
		http:         httpClient,
		userProject:  auth.QuotaProject,
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

// VertexAuth is the resolved answer to who Trivial authenticates as and which
// project it bills.
type VertexAuth struct {
	// TokenSource mints and refreshes the bearer token.
	TokenSource oauth2.TokenSource
	// Project is the GCP project Vertex requests are addressed to.
	Project string
	// ProjectFrom names where Project came from, for diagnostics.
	ProjectFrom string
	// Source describes the credential in terms a user can act on.
	Source string
	// QuotaProject is the project API usage is billed to, sent as
	// x-goog-user-project. It is only ever the quota_project_id the credentials
	// themselves name — see googleClient.userProject.
	QuotaProject string
}

// ResolveVertexAuth works out which credential answers and which project it
// addresses, without making a request.
//
// Every caller goes through here, so that `trivial config` reports exactly what
// `trivial serve` will use.
func ResolveVertexAuth(ctx context.Context, opts Options) (VertexAuth, error) {
	// Read before vertexCredentials, which overwrites it for an explicit path.
	ambient := os.Getenv(adcEnvVar)

	creds, err := vertexCredentials(ctx, opts.CredentialsFile)
	if err != nil {
		return VertexAuth{}, err
	}
	file := parseCredentialsJSON(creds.JSON)
	gcloud := readGcloudConfig()

	auth := VertexAuth{
		TokenSource:  creds.TokenSource,
		Source:       describeCredentials(opts.CredentialsFile, ambient, file, gcloud),
		QuotaProject: file.QuotaProjectID,
	}

	// A service account key and the metadata server both name their project;
	// the file gcloud's application-default login writes does not, so the rest
	// of this chain exists to find one for a user login.
	switch {
	case opts.Project != "":
		auth.Project, auth.ProjectFrom = opts.Project, "configuration"
	case creds.ProjectID != "":
		auth.Project, auth.ProjectFrom = creds.ProjectID, "the credentials"
	case file.QuotaProjectID != "":
		auth.Project, auth.ProjectFrom = file.QuotaProjectID, "the quota project"
	case os.Getenv(gcloudProjectEnvVar) != "":
		auth.Project, auth.ProjectFrom = os.Getenv(gcloudProjectEnvVar), gcloudProjectEnvVar
	case gcloud.Project != "":
		auth.Project, auth.ProjectFrom = gcloud.Project, "gcloud config"
	default:
		return VertexAuth{}, errNoVertexProject
	}
	return auth, nil
}

// errNoVertexProject names every way out, because the likeliest reader of it
// has a working gcloud login and no idea why that is not enough.
var errNoVertexProject = errors.New("vertex: no project ID.\n" +
	"  A gcloud user login does not name a project, so give it one:\n" +
	"    gcloud config set project <project>\n" +
	"    TRIVIAL_VERTEX_PROJECT=<project>   (or --vertex-project <project>)\n" +
	"  A service account key names its own: --vertex-credentials <key.json>")

// credentialsJSON is the part of a credentials file Trivial reads for itself —
// the fields x/oauth2 parses but does not expose on google.Credentials.
type credentialsJSON struct {
	Type           string `json:"type"`
	ClientEmail    string `json:"client_email"`
	QuotaProjectID string `json:"quota_project_id"`
}

func parseCredentialsJSON(raw []byte) credentialsJSON {
	var f credentialsJSON
	if len(raw) > 0 {
		// A credential that will not parse here still authenticates fine; this
		// only feeds diagnostics, so a failure is not worth an error.
		_ = json.Unmarshal(raw, &f)
	}
	return f
}

// describeCredentials says which credential answered and where it came from.
//
// gcloud's configured account is reported for a user login only when the login
// is the one gcloud itself wrote, and is labelled as coming from gcloud,
// because it is gcloud's CLI account rather than a field of the credential.
func describeCredentials(path, ambient string, file credentialsJSON, gcloud gcloudConfig) string {
	wellKnown := path == "" && ambient == ""

	var who string
	switch file.Type {
	case "service_account":
		who = "service account " + file.ClientEmail
	case "authorized_user":
		who = "gcloud user login"
		if wellKnown && gcloud.Account != "" {
			who += " (gcloud account " + gcloud.Account + ")"
		}
	case "":
		return "GCE/Cloud Run attached identity"
	default:
		who = file.Type
	}

	switch {
	case path != "":
		return who + " — " + path
	case ambient != "":
		return who + " — " + ambient + " (from " + adcEnvVar + ")"
	}
	return who + " — application default credentials"
}

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
		return nil, fmt.Errorf("%w: %w\n"+
			"  Sign in with:  gcloud auth application-default login\n"+
			"  Or name a service account key:  TRIVIAL_VERTEX_CREDENTIALS=<key.json>\n"+
			"  Or try the app with no credentials at all:  --llm-provider mock",
			ErrNoCredentials, err)
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
	auth, err := ResolveVertexAuth(ctx, opts)
	if err != nil {
		return nil, err
	}

	client := oauth2.NewClient(ctx, auth.TokenSource)
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
	// This URL names no project, so a user credential has nothing to bill
	// against unless the quota project says so.
	if auth.QuotaProject != "" {
		req.Header.Set(userProjectHeader, auth.QuotaProject)
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
