// Package cli defines Trivial's command-line interface (SPEC.md §11).
package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2/google"

	"github.com/ahaley/trivial/internal/config"
	"github.com/ahaley/trivial/internal/llm"
	"github.com/ahaley/trivial/internal/store"
)

// flags collects the global command-line settings before resolution.
var flags config.Flags

// verbose switches on debug logging.
var verbose bool

// Execute runs the CLI.
func Execute(version string) error {
	root := &cobra.Command{
		Use:   "trivial",
		Short: "Work out your trivia muscle",
		Long: "Trivial builds trivia decks on any subject with an LLM, quizzes you on them,\n" +
			"and uses spaced repetition to bring back what you keep getting wrong.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	pf := root.PersistentFlags()
	pf.StringVar(&flags.ConfigPath, "config", "", "path to config.toml")
	pf.StringVar(&flags.DBPath, "db", "", "path to the SQLite database")
	pf.StringVar(&flags.Provider, "llm-provider", "", `LLM provider: "gemini", "vertex" or "mock"`)
	pf.StringVar(&flags.Model, "llm-model", "", "model ID to generate and grade with")
	pf.StringVar(&flags.VertexProject, "vertex-project", "", "GCP project for the vertex provider")
	pf.StringVar(&flags.VertexLocation, "vertex-location", "", `Vertex region, or "global"`)
	pf.StringVar(&flags.VertexCredentials, "vertex-credentials", "",
		"path to a service account JSON key (default: application default credentials)")
	pf.BoolVarP(&verbose, "verbose", "v", false, "log debug detail")

	root.AddCommand(serveCmd(), generateCmd(), topicsCmd(), statsCmd(), configCmd(), modelsCmd())
	return root.Execute()
}

// setup resolves configuration and opens the database. Callers must close the
// returned store.
func setup() (config.Config, *store.Store, *slog.Logger, error) {
	cfg, err := config.Load(flags)
	if err != nil {
		return cfg, nil, nil, err
	}
	log := newLogger()
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return cfg, nil, log, err
	}
	return cfg, st, log, nil
}

func newLogger() *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	log := slog.New(h)
	slog.SetDefault(log)
	return log
}

// newLLM builds the configured provider.
//
// The context is deliberately context.Background() rather than the command's:
// the Vertex provider refreshes its access tokens against it for the life of
// the process, so a context cancelled at shutdown would be the wrong lifetime.
func newLLM(cfg config.Config) (llm.Client, error) {
	return llm.New(context.Background(), llm.Options{
		Provider: cfg.Provider,
		Model:    cfg.Model,
		APIKey:   cfg.APIKey,
		Endpoint: cfg.Endpoint,

		Project:         cfg.VertexProject,
		Location:        cfg.VertexLocation,
		CredentialsFile: cfg.VertexCredentials,
	})
}

// credentialSource reports where the Vertex credentials will come from, so a
// misconfiguration is diagnosable without making a billed request. configured
// is the resolved --vertex-credentials setting.
func credentialSource(configured string) string {
	if configured != "" {
		if _, err := os.Stat(configured); err != nil {
			return fmt.Sprintf("%s (MISSING)", configured)
		}
		return configured
	}
	// Nothing app-specific set, so report which part of the ADC chain answers.
	if p := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); p != "" {
		if _, err := os.Stat(p); err != nil {
			return fmt.Sprintf("%s (MISSING, from GOOGLE_APPLICATION_CREDENTIALS)", p)
		}
		return p + " (from GOOGLE_APPLICATION_CREDENTIALS)"
	}
	if _, err := google.FindDefaultCredentials(context.Background(),
		"https://www.googleapis.com/auth/cloud-platform"); err != nil {
		return "(none found — set TRIVIAL_VERTEX_CREDENTIALS)"
	}
	return "(application default credentials)"
}

// mask reduces a secret to a recognisable stub.
func mask(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "(set)"
	}
	return "(set, ending " + s[len(s)-4:] + ")"
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// configCmd prints the resolved configuration, which is the quickest way to see
// where settings are actually coming from.
func configCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Show the resolved configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(flags)
			if err != nil {
				return err
			}
			path, _ := config.DefaultConfigPath()
			fmt.Printf("config file   %s\n", path)
			fmt.Printf("database      %s\n", cfg.DBPath)
			fmt.Printf("host          %s\n", cfg.Host)
			fmt.Printf("provider      %s\n", cfg.Provider)
			fmt.Printf("model         %s\n", cfg.Model)

			switch cfg.Provider {
			case llm.ProviderVertex:
				fmt.Printf("project       %s\n", orDefault(cfg.VertexProject, "(from credentials)"))
				fmt.Printf("location      %s\n", cfg.VertexLocation)
				fmt.Printf("credentials   %s\n", credentialSource(cfg.VertexCredentials))
			case llm.ProviderMock:
				// The mock needs no credentials; saying so avoids a false alarm.
				fmt.Printf("credentials   (not needed)\n")
			default:
				fmt.Printf("api key       %s\n", orDefault(mask(cfg.APIKey), "(not set)"))
			}
			if cfg.Endpoint != "" {
				fmt.Printf("endpoint      %s (overridden)\n", cfg.Endpoint)
			}

			fmt.Printf("deck size     %d facts\n", cfg.TargetCount)
			fmt.Printf("session size  %d questions\n", cfg.SessionSize)
			return nil
		},
	}
}
