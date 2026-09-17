package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ahaley/trivial/internal/config"
	"github.com/ahaley/trivial/internal/llm"
)

// probeCandidates are tried when a project will not list its models. Model IDs
// change over time, so this is a starting point rather than an authority —
// pass IDs as arguments to check specific ones.
var probeCandidates = []string{
	"gemini-3.5-flash",
	"gemini-3.1-pro-preview",
	"gemini-3-flash",
	"gemini-3-pro",
	"gemini-2.5-flash",
	"gemini-2.5-pro",
	"gemini-2.0-flash",
}

func modelsCmd() *cobra.Command {
	var all, probe bool

	cmd := &cobra.Command{
		Use:   "models [model-id...]",
		Short: "Show which models your project can actually use",
		Long: "Asks your provider which models are available, so you know what to pass to\n" +
			"--llm-model. Naming a model ID checks that specific one by sending it a\n" +
			"one-word request, which is the only way to be certain it works.",
		Example: `  trivial models
  trivial models --probe
  trivial models gemini-2.5-flash gemini-2.0-flash`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(flags)
			if err != nil {
				return err
			}

			// Named models, or an explicit --probe, go straight to probing.
			if len(args) > 0 {
				return probeModels(cmd.Context(), cfg, args)
			}
			if probe {
				return probeModels(cmd.Context(), cfg, probeCandidates)
			}

			if cfg.Provider != llm.ProviderVertex {
				fmt.Printf("Listing is only supported for the vertex provider (this is %q).\n", cfg.Provider)
				fmt.Println("Checking the usual model IDs instead:")
				fmt.Println()
				return probeModels(cmd.Context(), cfg, probeCandidates)
			}

			models, err := llm.ListVertexModels(cmd.Context(), llm.Options{
				Project:         cfg.VertexProject,
				Location:        cfg.VertexLocation,
				CredentialsFile: cfg.VertexCredentials,
			})
			if err != nil {
				// Listing needs its own permission, which plenty of service
				// accounts lack even when generation works fine.
				fmt.Fprintf(os.Stderr, "Could not list models: %v\n\n", err)
				fmt.Println("Checking the usual model IDs directly instead:")
				fmt.Println()
				return probeModels(cmd.Context(), cfg, probeCandidates)
			}

			shown := 0
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "MODEL\tVERSION\tSTAGE")
			for _, m := range models {
				if !all && !strings.Contains(m.ID, "gemini") {
					continue
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", m.ID, orDefault(m.VersionID, "-"), orDefault(m.LaunchStage, "-"))
				shown++
			}
			w.Flush()

			switch {
			case shown == 0 && len(models) > 0:
				fmt.Printf("\nNo Gemini models in %s. Re-run with --all to see all %d models.\n",
					cfg.VertexLocation, len(models))
			case shown == 0:
				fmt.Printf("\nNo models reported for %s.\n", cfg.VertexLocation)
			default:
				fmt.Printf("\n%d models in %s. Set one with --llm-model, and confirm it works with:\n",
					shown, cfg.VertexLocation)
				fmt.Println("  trivial models <model-id>")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "include non-Gemini models")
	cmd.Flags().BoolVar(&probe, "probe", false, "check the usual model IDs instead of listing")
	return cmd
}

// probeModels sends each candidate a one-word request and reports which answer.
// Being listed is not the same as being usable, so this is the real test.
func probeModels(ctx context.Context, cfg config.Config, ids []string) error {
	type result struct {
		id  string
		err error
	}

	// A short timeout: a model that works answers "ok" almost immediately, and
	// a whole candidate list should not take minutes to rule out.
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	results := make([]result, len(ids))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)

	for i, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, id string) {
			defer wg.Done()
			defer func() { <-sem }()

			client, err := llm.New(ctx, llm.Options{
				Provider:        cfg.Provider,
				Model:           id,
				APIKey:          cfg.APIKey,
				Endpoint:        cfg.Endpoint,
				Project:         cfg.VertexProject,
				Location:        cfg.VertexLocation,
				CredentialsFile: cfg.VertexCredentials,
			})
			if err != nil {
				results[i] = result{id, err}
				return
			}
			results[i] = result{id, llm.Probe(ctx, client)}
		}(i, id)
	}
	wg.Wait()

	working := 0
	var unexplained []result
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, r := range results {
		if r.err == nil {
			fmt.Fprintf(w, "  ok\t%s\n", r.id)
			working++
			continue
		}
		short, known := reason(r.err)
		fmt.Fprintf(w, "  --\t%s\t%s\n", r.id, short)
		if !known {
			unexplained = append(unexplained, r)
		}
	}
	w.Flush()

	// A recognised failure explains itself in the table. Anything else has to
	// be shown in full, or the one line that says why is the line we cut off.
	for _, r := range unexplained {
		fmt.Printf("\n%s:\n  %s\n", r.id, strings.ReplaceAll(r.err.Error(), "\n", "\n  "))
	}

	if working == 0 {
		fmt.Println()
		if cfg.Provider == llm.ProviderVertex {
			fmt.Printf("Nothing answered in %s. Worth checking, in order:\n", cfg.VertexLocation)
			fmt.Println("  - the model exists in this region (try --vertex-location global)")
			fmt.Println("  - aiplatform.googleapis.com is enabled on the project")
			fmt.Println("  - the service account has roles/aiplatform.user")
		} else {
			fmt.Println("Nothing answered. Check the API key and model IDs, or run")
			fmt.Println("`trivial config` to see what settings are actually in effect.")
		}
		return nil
	}
	fmt.Printf("\n%d model(s) answered. Use one with:\n", working)
	fmt.Println("  trivial serve --llm-model <model-id>")
	fmt.Println("...or set TRIVIAL_LLM_MODEL so it sticks.")
	return nil
}

// reason condenses a provider error for the table, and reports whether it
// recognised the failure. An unrecognised error is printed in full afterwards
// rather than truncated into uselessness.
func reason(err error) (short string, known bool) {
	s := err.Error()
	switch {
	case strings.Contains(s, "model not available"), strings.Contains(s, " 404"):
		return "not available here", true
	case strings.Contains(s, " 403"), strings.Contains(s, "PERMISSION_DENIED"):
		return "no access", true
	case strings.Contains(s, " 429"), strings.Contains(s, "RESOURCE_EXHAUSTED"):
		return "quota exhausted", true
	case strings.Contains(s, " 503"), strings.Contains(s, "UNAVAILABLE"):
		return "overloaded", true
	case strings.Contains(s, "context deadline"):
		return "timed out", true
	}
	return "failed — see below", false
}
