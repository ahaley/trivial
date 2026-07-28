package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clearEnv removes every TRIVIAL_* variable so a developer's own settings
// cannot change the result of a test.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"TRIVIAL_CONFIG", "TRIVIAL_HOST", "TRIVIAL_DB_PATH", "TRIVIAL_LLM_PROVIDER",
		"TRIVIAL_LLM_MODEL", "TRIVIAL_LLM_API_KEY", "TRIVIAL_LLM_ENDPOINT",
		"TRIVIAL_VERTEX_PROJECT", "TRIVIAL_VERTEX_LOCATION", "TRIVIAL_VERTEX_CREDENTIALS",
		"GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION",
	} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	// Point the default config path somewhere empty.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
}

func TestDefaults(t *testing.T) {
	clearEnv(t)
	cfg, err := Load(Flags{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Host != DefaultHost {
		t.Errorf("host = %q, want %q", cfg.Host, DefaultHost)
	}
	if cfg.Model != DefaultModel || cfg.Provider != DefaultProvider {
		t.Errorf("provider/model = %q/%q", cfg.Provider, cfg.Model)
	}
	if cfg.TargetCount != DefaultTargetCount || cfg.SessionSize != DefaultSessionSize {
		t.Errorf("counts = %d/%d", cfg.TargetCount, cfg.SessionSize)
	}
	if !filepath.IsAbs(cfg.DBPath) {
		t.Errorf("db path %q should be absolute", cfg.DBPath)
	}
	if cfg.APIKey != "" {
		t.Error("no API key should be assumed")
	}
}

func TestPrecedence(t *testing.T) {
	clearEnv(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `
host = "file-host:1"
llm_model = "file-model"
llm_api_key = "file-key"
target_count = 11
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	// File alone.
	cfg, err := Load(Flags{ConfigPath: path})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Host != "file-host:1" || cfg.Model != "file-model" ||
		cfg.APIKey != "file-key" || cfg.TargetCount != 11 {
		t.Fatalf("file settings not applied: %+v", cfg)
	}

	// Environment beats the file.
	t.Setenv("TRIVIAL_HOST", "env-host:2")
	t.Setenv("TRIVIAL_LLM_MODEL", "env-model")
	cfg, err = Load(Flags{ConfigPath: path})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Host != "env-host:2" || cfg.Model != "env-model" {
		t.Errorf("environment should beat the file: %+v", cfg)
	}
	// Settings the environment did not touch still come from the file.
	if cfg.APIKey != "file-key" || cfg.TargetCount != 11 {
		t.Errorf("untouched file settings were lost: %+v", cfg)
	}

	// Flags beat everything.
	cfg, err = Load(Flags{ConfigPath: path, Host: "flag-host:3", Model: "flag-model", TargetCount: 42})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Host != "flag-host:3" || cfg.Model != "flag-model" || cfg.TargetCount != 42 {
		t.Errorf("flags should win: %+v", cfg)
	}
}

func TestMissingConfigFile(t *testing.T) {
	clearEnv(t)
	missing := filepath.Join(t.TempDir(), "nope.toml")

	// Named explicitly: the user meant it, so its absence is an error.
	if _, err := Load(Flags{ConfigPath: missing}); err == nil {
		t.Error("an explicitly named missing config should fail")
	}
	// Not named: the default path simply may not exist.
	if _, err := Load(Flags{}); err != nil {
		t.Errorf("a missing default config should be fine, got %v", err)
	}
}

func TestMalformedConfigFileIsAnError(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("host = \nnot toml at all ]["), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(Flags{ConfigPath: path})
	if err == nil {
		t.Fatal("a malformed config should not be silently ignored")
	}
	if !strings.Contains(err.Error(), "config") {
		t.Errorf("the error should name the config file, got %v", err)
	}
}

func TestVertexDefaults(t *testing.T) {
	clearEnv(t)
	cfg, err := Load(Flags{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.VertexLocation != DefaultVertexLocation {
		t.Errorf("location = %q, want %q", cfg.VertexLocation, DefaultVertexLocation)
	}
	if cfg.VertexProject != "" {
		t.Errorf("project should default to empty (taken from credentials), got %q", cfg.VertexProject)
	}
	// The endpoint must stay empty so each provider supplies its own base URL:
	// a default carried over from AI Studio would misroute Vertex requests.
	if cfg.Endpoint != "" {
		t.Errorf("endpoint should default to empty, got %q", cfg.Endpoint)
	}
}

// A shell already configured for gcloud should work without Trivial-specific
// variables, but ours must win where both are set.
func TestVertexEnvPrecedence(t *testing.T) {
	clearEnv(t)

	t.Setenv("GOOGLE_CLOUD_PROJECT", "gcloud-project")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "europe-west4")
	cfg, err := Load(Flags{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.VertexProject != "gcloud-project" || cfg.VertexLocation != "europe-west4" {
		t.Errorf("gcloud variables not picked up: %q / %q", cfg.VertexProject, cfg.VertexLocation)
	}

	t.Setenv("TRIVIAL_VERTEX_PROJECT", "trivial-project")
	cfg, err = Load(Flags{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.VertexProject != "trivial-project" {
		t.Errorf("TRIVIAL_VERTEX_PROJECT should win, got %q", cfg.VertexProject)
	}
	if cfg.VertexLocation != "europe-west4" {
		t.Errorf("the untouched gcloud location should survive, got %q", cfg.VertexLocation)
	}

	cfg, err = Load(Flags{VertexProject: "flag-project", VertexLocation: "us-central1"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.VertexProject != "flag-project" || cfg.VertexLocation != "us-central1" {
		t.Errorf("flags should win: %q / %q", cfg.VertexProject, cfg.VertexLocation)
	}
}

// The credentials path is Trivial's own setting, deliberately separate from the
// process-wide GOOGLE_APPLICATION_CREDENTIALS.
func TestVertexCredentialsPath(t *testing.T) {
	clearEnv(t)

	// Unset means "fall back to application default credentials", so the
	// ambient variable must not be copied into our setting.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/somewhere/else.json")
	cfg, err := Load(Flags{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.VertexCredentials != "" {
		t.Errorf("credentials should default to empty, got %q", cfg.VertexCredentials)
	}

	t.Setenv("TRIVIAL_VERTEX_CREDENTIALS", "/env/sa.json")
	cfg, err = Load(Flags{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.VertexCredentials != "/env/sa.json" {
		t.Errorf("credentials = %q, want the environment value", cfg.VertexCredentials)
	}

	cfg, err = Load(Flags{VertexCredentials: "/flag/sa.json"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.VertexCredentials != "/flag/sa.json" {
		t.Errorf("credentials = %q, want the flag to win", cfg.VertexCredentials)
	}
}

func TestZeroFlagsDoNotOverride(t *testing.T) {
	clearEnv(t)
	t.Setenv("TRIVIAL_HOST", "env-host:2")

	// Empty and zero flag values mean "not set", not "set to empty".
	cfg, err := Load(Flags{Host: "", TargetCount: 0})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Host != "env-host:2" {
		t.Errorf("an unset flag should not clobber the environment: %q", cfg.Host)
	}
	if cfg.TargetCount != DefaultTargetCount {
		t.Errorf("an unset flag should not clobber the default: %d", cfg.TargetCount)
	}
}
