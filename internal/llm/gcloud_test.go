package llm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGcloudConfigReadsCoreSection(t *testing.T) {
	dir := isolateADC(t)
	writeGcloudConfig(t, dir, "default", `[core]
account = someone@example.com
project = read-me

[compute]
region = us-central1
project = ignore-me
`)

	cfg := readGcloudConfig()
	if cfg.Project != "read-me" {
		t.Errorf("Project = %q, want the one in [core]", cfg.Project)
	}
	if cfg.Account != "someone@example.com" {
		t.Errorf("Account = %q", cfg.Account)
	}
}

func TestGcloudConfigFollowsActiveConfig(t *testing.T) {
	dir := isolateADC(t)
	writeGcloudConfig(t, dir, "default", "[core]\nproject = default-project\n")
	writeGcloudConfig(t, dir, "work", "[core]\nproject = work-project\n")
	if err := os.WriteFile(filepath.Join(dir, "active_config"), []byte("work\n"), 0o600); err != nil {
		t.Fatalf("write active_config: %v", err)
	}

	if got := readGcloudConfig().Project; got != "work-project" {
		t.Errorf("Project = %q, want the active configuration", got)
	}

	// The environment override beats the active_config file, as it does for
	// gcloud itself.
	t.Setenv("CLOUDSDK_ACTIVE_CONFIG_NAME", "default")
	if got := readGcloudConfig().Project; got != "default-project" {
		t.Errorf("Project = %q, want CLOUDSDK_ACTIVE_CONFIG_NAME to win", got)
	}
}

// A machine without gcloud is an ordinary case, not a failure.
func TestGcloudConfigAbsentIsEmpty(t *testing.T) {
	isolateADC(t)
	if cfg := readGcloudConfig(); cfg != (gcloudConfig{}) {
		t.Errorf("readGcloudConfig() = %+v, want zero", cfg)
	}
}

func TestGcloudProjectEnvVarWins(t *testing.T) {
	dir := isolateADC(t)
	writeUserADC(t, dir, "")
	writeGcloudConfig(t, dir, "default", "[core]\nproject = gcloud-project\n")
	t.Setenv(gcloudProjectEnvVar, "env-project")

	auth, err := ResolveVertexAuth(t.Context(), Options{})
	if err != nil {
		t.Fatalf("ResolveVertexAuth: %v", err)
	}
	if auth.Project != "env-project" {
		t.Errorf("Project = %q, want %s to beat the config file", auth.Project, gcloudProjectEnvVar)
	}
}
