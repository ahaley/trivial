package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ahaley/trivial/internal/generate"
	"github.com/ahaley/trivial/internal/model"
)

func generateCmd() *cobra.Command {
	var guidance string
	cmd := &cobra.Command{
		Use:   "generate <subject>",
		Short: "Research a subject and build a deck of questions",
		Long: "Runs the same generation pipeline the web interface uses, but in the\n" +
			"foreground so you can watch it and see any failure directly.",
		Example: `  trivial generate "The Roman Republic"
  trivial generate "Grand Slam tennis" --count 50 --guidance "focus on the open era"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, st, log, err := setup()
			if err != nil {
				return err
			}
			defer st.Close()

			client, err := newLLM(cfg)
			if err != nil {
				return err
			}

			subject := strings.TrimSpace(args[0])
			count := flags.TargetCount
			if count <= 0 {
				count = cfg.TargetCount
			}

			topic, err := st.CreateTopic(subject, guidance, count)
			if err != nil {
				return err
			}
			fmt.Printf("Generating %d questions on %q using %s…\n", count, subject, client.Name())

			start := time.Now()
			if err := generate.New(st, client, log).Run(cmd.Context(), topic); err != nil {
				return err
			}

			summary, err := st.TopicSummaryByID(topic.ID)
			if err != nil {
				return err
			}
			fmt.Printf("Done in %s: %d questions ready (topic %d).\n",
				time.Since(start).Round(time.Second), summary.FactCount, topic.ID)
			return nil
		},
	}
	cmd.Flags().IntVar(&flags.TargetCount, "count", 0, "how many facts to generate")
	cmd.Flags().StringVar(&guidance, "guidance", "", "extra direction for the model")
	return cmd
}

func topicsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "topics",
		Short: "List your topics and how well you know them",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, st, _, err := setup()
			if err != nil {
				return err
			}
			defer st.Close()

			topics, err := st.ListTopics()
			if err != nil {
				return err
			}
			if len(topics) == 0 {
				fmt.Println("No topics yet. Create one with:  trivial generate \"your subject\"")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tSUBJECT\tQUESTIONS\tNEW\tDUE\tMASTERY\tSTATUS")
			for _, t := range topics {
				status := string(t.Status)
				if t.Status == model.StatusGenerating {
					status = fmt.Sprintf("generating %d%%", t.Progress)
				}
				fmt.Fprintf(w, "%d\t%s\t%d\t%d\t%d\t%s\t%s\n",
					t.ID, truncate(t.Subject, 40), t.FactCount, t.NewCount, t.DueCount,
					percent(t.Mastery), status)
			}
			return w.Flush()
		},
	}
}

func statsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Show your due queue and weakest subtopics",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, st, _, err := setup()
			if err != nil {
				return err
			}
			defer st.Close()

			stats, err := st.Stats(30)
			if err != nil {
				return err
			}
			t := stats.Totals
			fmt.Printf("%d questions across %d topics — %s mastery\n",
				t.Facts, t.Topics, percent(t.Mastery))
			fmt.Printf("%d due now, %d never seen, %d already learned\n",
				t.DueNow, t.NewFacts, t.Seen)
			if t.Attempts > 0 {
				fmt.Printf("%d answers so far, %s correct", t.Attempts, percent(t.Accuracy))
				if t.StreakDay > 0 {
					fmt.Printf(" — %d day streak", t.StreakDay)
				}
				fmt.Println()
			}

			if len(stats.WeakTags) > 0 {
				fmt.Println("\nWeakest subtopics")
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				for _, tag := range stats.WeakTags {
					fmt.Fprintf(w, "  %s\t%s correct\t(%d answers)\n",
						tag.Tag, percent(tag.Accuracy), tag.Attempts)
				}
				w.Flush()
			}

			upcoming := 0
			for _, b := range stats.DueQueue {
				if !b.Overdue {
					upcoming += b.Count
				}
			}
			fmt.Printf("\n%d reviews scheduled over the next two weeks.\n", upcoming)
			return nil
		},
	}
}

func percent(v float64) string { return fmt.Sprintf("%.0f%%", v*100) }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
