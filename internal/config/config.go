// Package config resolves Trivial's runtime configuration.
//
// Precedence, highest first: command-line flags, environment variables,
// config file, built-in defaults (SPEC.md §11).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/ahaley/trivial/internal/llm"
)

// Defaults for the settings that have sensible built-in values. The
// provider-shaped ones live in package llm, which is where they are facts.
const (
	DefaultHost           = "localhost:1234"
	DefaultModel          = llm.DefaultModel
	DefaultProvider       = llm.ProviderVertex
	DefaultVertexLocation = llm.DefaultVertexLocation
	DefaultTargetCount    = 30
	DefaultSessionSize    = 20
)

// Config is the fully resolved configuration.
type Config struct {
	// Host is the listen address for `trivial serve`.
	Host string `toml:"host"`
	// DBPath is the SQLite file backing all state.
	DBPath string `toml:"db_path"`

	// Provider selects the LLM backend: "gemini", "vertex" or "mock".
	Provider string `toml:"llm_provider"`
	// Model is the model ID passed to the provider.
	Model string `toml:"llm_model"`
	// APIKey authenticates the "gemini" provider. Vertex uses Application
	// Default Credentials instead and ignores this.
	APIKey string `toml:"llm_api_key"`
	// Endpoint overrides the provider's base URL. Left empty by default: the
	// right base URL differs per provider, so each one supplies its own.
	Endpoint string `toml:"llm_endpoint"`

	// VertexProject is the GCP project billed for Vertex requests. Empty means
	// "whichever project the credentials name".
	VertexProject string `toml:"vertex_project"`
	// VertexLocation is the Vertex region, or "global".
	VertexLocation string `toml:"vertex_location"`
	// VertexCredentials is a path to a service account JSON key. Empty falls
	// back to Application Default Credentials, which includes
	// GOOGLE_APPLICATION_CREDENTIALS, a gcloud login, or an attached identity.
	VertexCredentials string `toml:"vertex_credentials"`

	// TargetCount is the default number of facts generated per topic.
	TargetCount int `toml:"target_count"`
	// SessionSize caps the number of questions in one session.
	SessionSize int `toml:"session_size"`
}

// Flags holds values supplied on the command line. Zero values mean "not set",
// so they fall through to the next source in the precedence chain.
type Flags struct {
	Host              string
	DBPath            string
	Provider          string
	Model             string
	ConfigPath        string
	VertexProject     string
	VertexLocation    string
	VertexCredentials string
	TargetCount       int
	SessionSize       int
}

// Load resolves configuration from flags, environment, config file and defaults.
func Load(f Flags) (Config, error) {
	c := Config{
		Host:           DefaultHost,
		Provider:       DefaultProvider,
		Model:          DefaultModel,
		VertexLocation: DefaultVertexLocation,
		TargetCount:    DefaultTargetCount,
		SessionSize:    DefaultSessionSize,
	}

	dbDefault, err := defaultDBPath()
	if err != nil {
		return Config{}, err
	}
	c.DBPath = dbDefault

	// Config file (lowest precedence above defaults).
	path := f.ConfigPath
	if path == "" {
		path = os.Getenv("TRIVIAL_CONFIG")
	}
	explicit := path != ""
	if path == "" {
		if p, err := DefaultConfigPath(); err == nil {
			path = p
		}
	}
	if path != "" {
		if err := mergeFile(&c, path, explicit); err != nil {
			return Config{}, err
		}
	}

	// Environment.
	setString(&c.Host, os.Getenv("TRIVIAL_HOST"))
	setString(&c.DBPath, os.Getenv("TRIVIAL_DB_PATH"))
	setString(&c.Provider, os.Getenv("TRIVIAL_LLM_PROVIDER"))
	setString(&c.Model, os.Getenv("TRIVIAL_LLM_MODEL"))
	setString(&c.APIKey, os.Getenv("TRIVIAL_LLM_API_KEY"))
	setString(&c.Endpoint, os.Getenv("TRIVIAL_LLM_ENDPOINT"))
	// The conventional gcloud variables first, so an already-configured shell
	// needs no Trivial-specific setup — then ours, which are more specific and
	// so must be able to override them.
	setString(&c.VertexProject, os.Getenv("GOOGLE_CLOUD_PROJECT"))
	setString(&c.VertexLocation, os.Getenv("GOOGLE_CLOUD_LOCATION"))
	setString(&c.VertexProject, os.Getenv("TRIVIAL_VERTEX_PROJECT"))
	setString(&c.VertexLocation, os.Getenv("TRIVIAL_VERTEX_LOCATION"))
	setString(&c.VertexCredentials, os.Getenv("TRIVIAL_VERTEX_CREDENTIALS"))

	// Flags (highest precedence).
	setString(&c.Host, f.Host)
	setString(&c.DBPath, f.DBPath)
	setString(&c.Provider, f.Provider)
	setString(&c.Model, f.Model)
	setString(&c.VertexProject, f.VertexProject)
	setString(&c.VertexLocation, f.VertexLocation)
	setString(&c.VertexCredentials, f.VertexCredentials)
	setInt(&c.TargetCount, f.TargetCount)
	setInt(&c.SessionSize, f.SessionSize)

	if c.DBPath != "" {
		abs, err := filepath.Abs(os.ExpandEnv(c.DBPath))
		if err != nil {
			return Config{}, fmt.Errorf("resolve db path: %w", err)
		}
		c.DBPath = abs
	}
	return c, nil
}

// mergeFile layers a TOML config file over c. A missing file is only an error
// when the user named it explicitly.
func mergeFile(c *Config, path string, explicit bool) error {
	var fileCfg Config
	if _, err := toml.DecodeFile(path, &fileCfg); err != nil {
		if errors.Is(err, os.ErrNotExist) && !explicit {
			return nil
		}
		return fmt.Errorf("read config %s: %w", path, err)
	}
	setString(&c.Host, fileCfg.Host)
	setString(&c.DBPath, fileCfg.DBPath)
	setString(&c.Provider, fileCfg.Provider)
	setString(&c.Model, fileCfg.Model)
	setString(&c.APIKey, fileCfg.APIKey)
	setString(&c.Endpoint, fileCfg.Endpoint)
	setString(&c.VertexProject, fileCfg.VertexProject)
	setString(&c.VertexLocation, fileCfg.VertexLocation)
	setString(&c.VertexCredentials, fileCfg.VertexCredentials)
	setInt(&c.TargetCount, fileCfg.TargetCount)
	setInt(&c.SessionSize, fileCfg.SessionSize)
	return nil
}

// DefaultConfigPath is <user config dir>/trivial/config.toml.
func DefaultConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "trivial", "config.toml"), nil
}

// defaultDBPath keeps state alongside the user's other application data.
func defaultDBPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", fmt.Errorf("locate data directory: %w", err)
		}
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "trivial", "trivial.db"), nil
}

func setString(dst *string, v string) {
	if v != "" {
		*dst = v
	}
}

func setInt(dst *int, v int) {
	if v > 0 {
		*dst = v
	}
}
