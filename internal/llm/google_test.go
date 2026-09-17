package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// capture records what the fake Google endpoint received.
type capture struct {
	path        string
	apiKey      string
	auth        string
	userProject string
	body        geminiRequest
}

// stubServer answers with reply, recording the request. status codes are
// consumed in order, so a test can make the first call fail and the next
// succeed.
func stubServer(t *testing.T, got *capture, reply string, statuses ...int) *httptest.Server {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		got.apiKey = r.Header.Get("x-goog-api-key")
		got.auth = r.Header.Get("Authorization")
		got.userProject = r.Header.Get(userProjectHeader)
		json.NewDecoder(r.Body).Decode(&got.body)

		n := int(calls.Add(1)) - 1
		status := http.StatusOK
		if n < len(statuses) {
			status = statuses[n]
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			json.NewEncoder(w).Encode(map[string]any{
				"candidates": []any{map[string]any{
					"content":      map[string]any{"parts": []any{map[string]string{"text": reply}}},
					"finishReason": "STOP",
				}},
			})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sampleRequest() Request {
	return Request{
		Task: TaskResearch, System: "you are a trivia author", Prompt: "write 3 facts",
		Schema: &Schema{Type: TypeObject}, Temperature: 0.9, MaxTokens: 2048,
	}
}

func TestGeminiSendsAPIKey(t *testing.T) {
	var got capture
	srv := stubServer(t, &got, `{"ok":true}`)

	client := NewGemini(Options{Model: "gemini-test", APIKey: "secret-key", Endpoint: srv.URL})
	out, err := client.Complete(context.Background(), sampleRequest())
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if out != `{"ok":true}` {
		t.Errorf("reply = %q", out)
	}
	if got.apiKey != "secret-key" {
		t.Errorf("x-goog-api-key = %q, want the configured key", got.apiKey)
	}
	if got.auth != "" {
		t.Errorf("AI Studio should not send an Authorization header, got %q", got.auth)
	}
	if got.path != "/models/gemini-test:generateContent" {
		t.Errorf("path = %q", got.path)
	}
	if !strings.Contains(client.Name(), "gemini-test") {
		t.Errorf("Name() = %q, should name the model", client.Name())
	}
}

// Vertex authenticates with a bearer token and must not send an API key.
func TestVertexSendsBearerToken(t *testing.T) {
	var got capture
	srv := stubServer(t, &got, `{"ok":true}`)

	// A static token stands in for the service account's refreshing source;
	// everything downstream of the token is identical.
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "test-token", TokenType: "Bearer"})
	httpClient := oauth2.NewClient(context.Background(), ts)
	httpClient.Timeout = 10 * time.Second

	client := &googleClient{
		name: "vertex/gemini-test",
		url:  VertexURL(srv.URL+"/v1", "my-project", "us-central1", "gemini-test"),
		http: httpClient,
	}
	if _, err := client.Complete(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if got.auth != "Bearer test-token" {
		t.Errorf("Authorization = %q, want a bearer token", got.auth)
	}
	if got.apiKey != "" {
		t.Errorf("Vertex should not send an API key, got %q", got.apiKey)
	}
	want := "/v1/projects/my-project/locations/us-central1/publishers/google/models/gemini-test:generateContent"
	if got.path != want {
		t.Errorf("path = %q, want %q", got.path, want)
	}
}

// serviceAccountJSON is a syntactically valid key. The private key is a real
// but throwaway 512-bit RSA key: the credentials only have to parse, since no
// test here exchanges a token.
const serviceAccountJSON = `{
  "type": "service_account",
  "project_id": "%s",
  "private_key_id": "abc123",
  "private_key": "-----BEGIN PRIVATE KEY-----\nMIIBVQIBADANBgkqhkiG9w0BAQEFAASCAT8wggE7AgEAAkEAwU3QDhIQUCC7Rn8x\nOZ5uKQaJmDXqNfBmwZ0J2GLd0e9RRXHIEfyMHNBaxrfBaCZv2NX3xcTLfRSPqmnu\nvxUYCQIDAQABAkAaKAiPvVfLLmYzpCiXBWMSIJIcVDkGmALP1nBRlHXMKUvQxfJH\nBpFStkKcFbBJmIVJ0eBaZ9NNKzOzOxDh2XCBAiEA9Wj9F3aXLJqLM1lXOZDmVxLl\nEQwGZ0j0S1BXnzeYQVkCIQDJoxRBmXjcvVJXlZAxTKuMBEHVBOr1QMzKUCcPzGYp\n0QIhAKmR0nSAKt7f4wJhLxA5eXqBjPKHKlDXbfeSXnkPRvNZAiBnZ0m7yFCJZ8Cx\n8IjKPYGXvSHNVFhYBGD9DWlkRLPqoQIhAOQFXCvhKLKvVqLLwGxjkMJHXtVBnBRV\nfPtMcUAJqBnZ\n-----END PRIVATE KEY-----\n",
  "client_email": "trivial@%s.iam.gserviceaccount.com",
  "client_id": "1234567890",
  "token_uri": "https://oauth2.googleapis.com/token"
}`

func writeServiceAccount(t *testing.T, project string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sa.json")
	body := fmt.Sprintf(serviceAccountJSON, project, project)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write service account: %v", err)
	}
	return path
}

// isolateADC clears the ambient credentials environment and restores it
// afterwards, returning the empty directory the tests may write a fake gcloud
// installation into. NewVertex writes GOOGLE_APPLICATION_CREDENTIALS when given
// an explicit path, so without this a test would leak into whichever runs next.
//
// APPDATA and HOME are redirected as well as CLOUDSDK_CONFIG: x/oauth2 looks
// for the application-default login under those two directly and ignores
// CLOUDSDK_CONFIG, so on a machine with a real gcloud login the tests would
// otherwise authenticate as whoever is sitting at it.
func isolateADC(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("CLOUDSDK_CONFIG", dir)
	t.Setenv("CLOUDSDK_CORE_PROJECT", "")
	t.Setenv("CLOUDSDK_ACTIVE_CONFIG_NAME", "")
	t.Setenv("APPDATA", dir)
	t.Setenv("HOME", dir)
	return dir
}

// writeGcloudConfig lays down a gcloud configuration inside an isolated
// CLOUDSDK_CONFIG directory.
func writeGcloudConfig(t *testing.T, dir, name, body string) {
	t.Helper()
	confDir := filepath.Join(dir, "configurations")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatalf("create gcloud config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "config_"+name), []byte(body), 0o600); err != nil {
		t.Fatalf("write gcloud config: %v", err)
	}
}

// userADCJSON is the shape `gcloud auth application-default login` writes.
// Note what it does not have: a project_id. The refresh token is never
// exchanged, because the token source is only built lazily.
const userADCJSON = `{
  "type": "authorized_user",
  "client_id": "000000000000-notarealclient.apps.googleusercontent.com",
  "client_secret": "not-a-real-secret",
  "refresh_token": "1//not-a-real-refresh-token"%s
}`

// writeUserADC writes a gcloud user login at the well-known ADC path, with an
// optional quota project.
func writeUserADC(t *testing.T, dir, quotaProject string) {
	t.Helper()
	gcloudDir := filepath.Join(dir, "gcloud")
	if err := os.MkdirAll(gcloudDir, 0o755); err != nil {
		t.Fatalf("create gcloud dir: %v", err)
	}
	quota := ""
	if quotaProject != "" {
		quota = fmt.Sprintf(",\n  %q: %q", "quota_project_id", quotaProject)
	}
	body := fmt.Sprintf(userADCJSON, quota)
	path := filepath.Join(gcloudDir, "application_default_credentials.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write user ADC: %v", err)
	}
	// x/oauth2 reads %APPDATA%/gcloud on Windows and $HOME/.config/gcloud
	// elsewhere; write both so the test does not depend on GOOS.
	unixDir := filepath.Join(dir, ".config", "gcloud")
	if err := os.MkdirAll(unixDir, 0o755); err != nil {
		t.Fatalf("create gcloud dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(unixDir, "application_default_credentials.json"),
		[]byte(body), 0o600); err != nil {
		t.Fatalf("write user ADC: %v", err)
	}
}

// The app-specific credentials path must take precedence over the ambient
// GOOGLE_APPLICATION_CREDENTIALS, which may already point at something else.
func TestVertexCredentialsFileWins(t *testing.T) {
	isolateADC(t)
	ambient := writeServiceAccount(t, "ambient-project")
	ours := writeServiceAccount(t, "our-project")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", ambient)

	client, err := NewVertex(context.Background(), Options{
		Model: "m", Location: "us-central1", CredentialsFile: ours,
	})
	if err != nil {
		t.Fatalf("NewVertex: %v", err)
	}
	if !strings.Contains(client.Name(), "our-project") {
		t.Errorf("Name() = %q, want the project from the configured key", client.Name())
	}
}

// With nothing app-specific set, the standard ADC chain still applies.
func TestVertexFallsBackToADC(t *testing.T) {
	isolateADC(t)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", writeServiceAccount(t, "ambient-project"))

	client, err := NewVertex(context.Background(), Options{Model: "m", Location: "us-central1"})
	if err != nil {
		t.Fatalf("NewVertex: %v", err)
	}
	if !strings.Contains(client.Name(), "ambient-project") {
		t.Errorf("Name() = %q, want the ADC project", client.Name())
	}
}

// An explicit project overrides the one named by the credentials.
func TestVertexExplicitProjectWins(t *testing.T) {
	isolateADC(t)
	client, err := NewVertex(context.Background(), Options{
		Model: "m", Location: "europe-west4", Project: "explicit-project",
		CredentialsFile: writeServiceAccount(t, "key-project"),
	})
	if err != nil {
		t.Fatalf("NewVertex: %v", err)
	}
	if !strings.Contains(client.Name(), "explicit-project") {
		t.Errorf("Name() = %q, want the explicit project", client.Name())
	}
	if !strings.Contains(client.Name(), "europe-west4") {
		t.Errorf("Name() = %q, should report the region", client.Name())
	}
}

func TestVertexRejectsBadCredentialsPath(t *testing.T) {
	isolateADC(t)
	_, err := NewVertex(context.Background(), Options{
		Model: "m", CredentialsFile: filepath.Join(t.TempDir(), "nope.json"),
	})
	if err == nil {
		t.Fatal("a missing credentials file should fail at construction")
	}
	if !errors.Is(err, ErrNoCredentials) {
		t.Errorf("error should wrap ErrNoCredentials, got %v", err)
	}
	// The message must name the path, or the user cannot fix it.
	if !strings.Contains(err.Error(), "nope.json") {
		t.Errorf("error should name the file, got %v", err)
	}
}

// A 404 from a named region should point at the global endpoint, since that is
// the usual cause and the usual fix.
func TestNotFoundHintNamesTheRegion(t *testing.T) {
	var got capture
	srv := stubServer(t, &got, "", http.StatusNotFound)

	client := &googleClient{
		name:         "vertex/some-model (proj, us-central1)",
		url:          srv.URL,
		http:         srv.Client(),
		notFoundHint: notFoundHint("us-central1"),
	}
	_, err := client.Complete(context.Background(), sampleRequest())
	if err == nil {
		t.Fatal("expected a 404 error")
	}
	for _, want := range []string{"us-central1", "global", "TRIVIAL_VERTEX_LOCATION"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got %v", want, err)
		}
	}

	// At global there is no region to blame, so the hint must not suggest one.
	if h := notFoundHint("global"); strings.Contains(h, "--vertex-location") {
		t.Errorf("the global hint should not suggest switching region: %q", h)
	}
}

func TestVertexURL(t *testing.T) {
	tests := []struct {
		name                               string
		endpoint, project, location, model string
		want                               string
	}{
		{
			name:    "regional",
			project: "proj", location: "us-central1", model: "gemini-3.6-flash",
			want: "https://us-central1-aiplatform.googleapis.com/v1/projects/proj/locations/us-central1" +
				"/publishers/google/models/gemini-3.6-flash:generateContent",
		},
		{
			// The global endpoint is the unprefixed host, not "global-...".
			name:    "global",
			project: "proj", location: "global", model: "gemini-3.6-flash",
			want: "https://aiplatform.googleapis.com/v1/projects/proj/locations/global" +
				"/publishers/google/models/gemini-3.6-flash:generateContent",
		},
		{
			name:     "explicit endpoint wins, trailing slash trimmed",
			endpoint: "https://example.test/v1/", project: "proj", location: "us-central1", model: "m",
			want: "https://example.test/v1/projects/proj/locations/us-central1/publishers/google/models/m:generateContent",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := VertexURL(tc.endpoint, tc.project, tc.location, tc.model); got != tc.want {
				t.Errorf("VertexURL = %q\nwant %q", got, tc.want)
			}
		})
	}
}

// Both providers must produce the same request body — that shared shape is why
// they can share a client at all.
func TestRequestBodyShape(t *testing.T) {
	var got capture
	srv := stubServer(t, &got, `{}`)

	client := NewGemini(Options{Model: "m", APIKey: "k", Endpoint: srv.URL})
	if _, err := client.Complete(context.Background(), sampleRequest()); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if len(got.body.Contents) != 1 || got.body.Contents[0].Parts[0].Text != "write 3 facts" {
		t.Errorf("prompt not sent as user content: %+v", got.body.Contents)
	}
	if got.body.SystemInstruction == nil ||
		got.body.SystemInstruction.Parts[0].Text != "you are a trivia author" {
		t.Errorf("system instruction not sent: %+v", got.body.SystemInstruction)
	}
	cfg := got.body.GenerationConfig
	if cfg == nil || cfg.MaxOutputTokens != 2048 || cfg.Temperature == nil || *cfg.Temperature != 0.9 {
		t.Errorf("generation config wrong: %+v", cfg)
	}
	// A schema must switch the response to constrained JSON.
	if cfg.ResponseMIMEType != "application/json" || cfg.ResponseSchema == nil {
		t.Errorf("structured output not requested: %+v", cfg)
	}
}

func TestNoSchemaLeavesResponseUnconstrained(t *testing.T) {
	var got capture
	srv := stubServer(t, &got, "plain text")

	client := NewGemini(Options{Model: "m", APIKey: "k", Endpoint: srv.URL})
	req := sampleRequest()
	req.Schema = nil
	if _, err := client.Complete(context.Background(), req); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if got.body.GenerationConfig.ResponseMIMEType != "" {
		t.Errorf("no schema should not force a JSON MIME type")
	}
}

func TestRetriesServerErrorsButNotClientErrors(t *testing.T) {
	var got capture
	// First call 503, second succeeds.
	srv := stubServer(t, &got, `{"ok":true}`, http.StatusServiceUnavailable, http.StatusOK)
	client := NewGemini(Options{Model: "m", APIKey: "k", Endpoint: srv.URL})
	if _, err := client.Complete(context.Background(), sampleRequest()); err != nil {
		t.Errorf("a 503 should be retried, got %v", err)
	}

	// A 400 is the caller's fault and must fail immediately.
	var got2 capture
	srv2 := stubServer(t, &got2, `{}`, http.StatusBadRequest, http.StatusOK)
	client2 := NewGemini(Options{Model: "m", APIKey: "k", Endpoint: srv2.URL})
	_, err := client2.Complete(context.Background(), sampleRequest())
	if err == nil {
		t.Fatal("a 400 should not be retried into success")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should report the status, got %v", err)
	}
}

// The defaults are what a fresh install talks to, and getting either wrong is a
// 404 that looks like an auth problem. Pin them.
func TestDefaultsMatchAWorkingVertexSetup(t *testing.T) {
	if DefaultVertexLocation != "global" {
		t.Errorf("default location = %q; Gemini models are served from the global "+
			"endpoint far more consistently than from any named region", DefaultVertexLocation)
	}
	if !strings.HasPrefix(DefaultModel, "gemini-") {
		t.Errorf("default model = %q, want a Gemini model ID", DefaultModel)
	}
	// The default provider must need no API key, or a fresh install cannot run.
	if _, err := New(context.Background(), Options{Provider: ""}); errors.Is(err, ErrNoAPIKey) {
		t.Error("the default provider should not require an API key")
	}
}

func TestNewSelectsProvider(t *testing.T) {
	ctx := context.Background()

	if c, err := New(ctx, Options{Provider: "mock"}); err != nil || c.Name() != "mock" {
		t.Errorf("mock: %v / %v", c, err)
	}
	if c, err := New(ctx, Options{Provider: "gemini", APIKey: "k"}); err != nil ||
		!strings.HasPrefix(c.Name(), "gemini/") {
		t.Errorf("gemini: %v / %v", c, err)
	}
	// A missing key must name both what to set and the default alternative.
	_, err := New(ctx, Options{Provider: "gemini"})
	if err == nil {
		t.Fatal("gemini without a key should fail")
	}
	for _, want := range []string{"TRIVIAL_LLM_API_KEY", "vertex"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got %v", want, err)
		}
	}
	if _, err := New(ctx, Options{Provider: "nonsense"}); err == nil {
		t.Error("an unknown provider should fail")
	}
}

// The file a gcloud login writes names no project, so without a fallback a
// perfectly good login cannot address Vertex at all.
func TestVertexTakesProjectFromGcloudConfigForUserLogin(t *testing.T) {
	dir := isolateADC(t)
	writeUserADC(t, dir, "")
	writeGcloudConfig(t, dir, "default", "[core]\naccount = someone@example.com\nproject = gcloud-project\n")

	client, err := NewVertex(context.Background(), Options{Model: "m", Location: "us-central1"})
	if err != nil {
		t.Fatalf("NewVertex: %v", err)
	}
	if !strings.Contains(client.Name(), "gcloud-project") {
		t.Errorf("Name() = %q, want the project from gcloud config", client.Name())
	}
}

func TestVertexPrefersQuotaProjectOverGcloudConfig(t *testing.T) {
	dir := isolateADC(t)
	writeUserADC(t, dir, "quota-project")
	writeGcloudConfig(t, dir, "default", "[core]\nproject = gcloud-project\n")

	auth, err := ResolveVertexAuth(context.Background(), Options{})
	if err != nil {
		t.Fatalf("ResolveVertexAuth: %v", err)
	}
	if auth.Project != "quota-project" {
		t.Errorf("Project = %q, want the credentials quota project", auth.Project)
	}
	if auth.QuotaProject != "quota-project" {
		t.Errorf("QuotaProject = %q, want it carried through for the billing header", auth.QuotaProject)
	}
}

func TestVertexExplicitProjectBeatsGcloudConfig(t *testing.T) {
	dir := isolateADC(t)
	writeUserADC(t, dir, "quota-project")
	writeGcloudConfig(t, dir, "default", "[core]\nproject = gcloud-project\n")

	auth, err := ResolveVertexAuth(context.Background(), Options{Project: "explicit-project"})
	if err != nil {
		t.Fatalf("ResolveVertexAuth: %v", err)
	}
	if auth.Project != "explicit-project" {
		t.Errorf("Project = %q, want the configured project", auth.Project)
	}
}

// The error a user login with no project hits has to name every way out of it,
// because "your credentials are fine but nameless" is not a guessable state.
func TestVertexWithNoProjectAnywhereNamesTheFixes(t *testing.T) {
	dir := isolateADC(t)
	writeUserADC(t, dir, "")

	_, err := NewVertex(context.Background(), Options{Model: "m"})
	if err == nil {
		t.Fatal("a credential naming no project should fail at construction")
	}
	for _, want := range []string{"gcloud config set project", "TRIVIAL_VERTEX_PROJECT", "--vertex-credentials"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got:\n%s", want, err)
		}
	}
}

// A user login reports itself as one, so the user can tell at a glance whether
// the credential in play is the one they think it is.
func TestCredentialSourceNamesTheGcloudAccount(t *testing.T) {
	dir := isolateADC(t)
	writeUserADC(t, dir, "")
	writeGcloudConfig(t, dir, "default", "[core]\naccount = someone@example.com\nproject = gcloud-project\n")

	auth, err := ResolveVertexAuth(context.Background(), Options{})
	if err != nil {
		t.Fatalf("ResolveVertexAuth: %v", err)
	}
	if !strings.Contains(auth.Source, "gcloud user login") {
		t.Errorf("Source = %q, want it to name the gcloud login", auth.Source)
	}
	if !strings.Contains(auth.Source, "someone@example.com") {
		t.Errorf("Source = %q, want it to name the account", auth.Source)
	}
	if auth.ProjectFrom != "gcloud config" {
		t.Errorf("ProjectFrom = %q, want %q", auth.ProjectFrom, "gcloud config")
	}
}

func TestCredentialSourceNamesTheServiceAccount(t *testing.T) {
	isolateADC(t)
	auth, err := ResolveVertexAuth(context.Background(), Options{
		CredentialsFile: writeServiceAccount(t, "key-project"),
	})
	if err != nil {
		t.Fatalf("ResolveVertexAuth: %v", err)
	}
	if !strings.Contains(auth.Source, "service account trivial@key-project.iam.gserviceaccount.com") {
		t.Errorf("Source = %q, want it to name the service account", auth.Source)
	}
	if auth.QuotaProject != "" {
		t.Errorf("QuotaProject = %q, a service account key names none", auth.QuotaProject)
	}
}

// A quota project is only sent when the credentials name one: inventing a value
// for x-goog-user-project needs a permission the caller may not have, and would
// turn a working request into a 403.
func TestQuotaProjectIsSentOnlyWhenConfigured(t *testing.T) {
	for _, tc := range []struct{ name, userProject, want string }{
		{"configured", "quota-project", "quota-project"},
		{"absent", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got capture
			srv := stubServer(t, &got, `{"ok":true}`)

			client := &googleClient{
				name:        "vertex/gemini-test",
				url:         VertexURL(srv.URL+"/v1", "p", "global", "gemini-test"),
				http:        srv.Client(),
				userProject: tc.userProject,
			}
			if _, err := client.Complete(context.Background(), sampleRequest()); err != nil {
				t.Fatalf("complete: %v", err)
			}
			if got.userProject != tc.want {
				t.Errorf("%s = %q, want %q", userProjectHeader, got.userProject, tc.want)
			}
		})
	}
}

// The one 403 that is not an IAM problem must not be reported as one.
func TestQuotaProjectForbiddenExplainsTheFix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"code":403,"message":"Your application is authenticating by using ` +
			`local Application Default Credentials. The aiplatform.googleapis.com API requires a quota project."}}`))
	}))
	t.Cleanup(srv.Close)

	client := &googleClient{
		name: "vertex/gemini-test",
		url:  VertexURL(srv.URL+"/v1", "p", "global", "gemini-test"),
		http: srv.Client(),
	}
	_, err := client.Complete(context.Background(), sampleRequest())
	if err == nil {
		t.Fatal("a 403 should fail")
	}
	if !strings.Contains(err.Error(), "set-quota-project") {
		t.Errorf("error should name the fix, got: %v", err)
	}
}
