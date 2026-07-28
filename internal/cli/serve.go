package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/ahaley/trivial/internal/server"
	"github.com/ahaley/trivial/web"
)

func serveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the Trivial web application",
		Long: "Starts the HTTP server and serves the web interface.\n\n" +
			"Bind to localhost for this machine only, or to your tailnet address to\n" +
			"reach it from your other devices.",
		Example: "  trivial serve --host localhost:1234",
		Args:    cobra.NoArgs,
		RunE:    runServe,
	}
	cmd.Flags().StringVar(&flags.Host, "host", "", "address to listen on (default localhost:1234)")
	cmd.Flags().IntVar(&flags.SessionSize, "session-size", 0, "questions per session")
	return cmd
}

func runServe(cmd *cobra.Command, args []string) error {
	cfg, st, log, err := setup()
	if err != nil {
		return err
	}
	defer st.Close()

	// A generation job cannot survive a restart, so anything left mid-flight
	// is marked failed rather than polling forever.
	if err := st.ResetStuckGenerations(); err != nil {
		log.Warn("could not reset interrupted generations", "err", err)
	}

	client, err := newLLM(cfg)
	if err != nil {
		return err
	}

	srv := server.New(cfg, st, client, log)
	httpSrv := &http.Server{
		Addr:              cfg.Host,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() {
		log.Info("trivial is listening",
			"addr", cfg.Host, "db", cfg.DBPath, "llm", client.Name())
		fmt.Printf("\n  Trivial is running at http://%s\n", cfg.Host)
		if !web.Built() {
			fmt.Printf("  The web interface has not been built — run `npm install && npm run build` in web/.\n")
		}
		fmt.Printf("  Press Ctrl-C to stop.\n\n")

		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		return fmt.Errorf("server: %w", err)
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Warn("shutdown was not clean", "err", err)
	}
	srv.Shutdown()
	return nil
}
