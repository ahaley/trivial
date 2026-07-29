// Package server exposes Trivial's REST API and serves the embedded frontend
// (SPEC.md §8, §9).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/ahaley/trivial/internal/config"
	"github.com/ahaley/trivial/internal/fsrs"
	"github.com/ahaley/trivial/internal/generate"
	"github.com/ahaley/trivial/internal/llm"
	"github.com/ahaley/trivial/internal/pedagogy"
	"github.com/ahaley/trivial/internal/store"
	"github.com/ahaley/trivial/internal/tutor"
	"github.com/ahaley/trivial/web"
)

// Server wires the API together.
type Server struct {
	cfg      config.Config
	store    *store.Store
	gen      *generate.Generator
	tutor    *tutor.Tutor
	sched    *fsrs.Scheduler
	composer *pedagogy.Composer
	log      *slog.Logger

	// jobs tracks in-flight generation so shutdown can cancel it.
	jobs sync.WaitGroup
	// baseCtx is cancelled on shutdown, stopping background generation.
	baseCtx    context.Context
	cancelJobs context.CancelFunc
}

// New builds a Server.
func New(cfg config.Config, st *store.Store, client llm.Client, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		cfg:        cfg,
		store:      st,
		gen:        generate.New(st, client, log),
		tutor:      tutor.New(client, log),
		sched:      fsrs.NewScheduler(),
		composer:   pedagogy.NewComposer(),
		log:        log,
		baseCtx:    ctx,
		cancelJobs: cancel,
	}
}

// Shutdown cancels background generation and waits for it to stop.
func (s *Server) Shutdown() {
	s.cancelJobs()
	s.jobs.Wait()
}

// Handler returns the fully routed HTTP handler.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(s.logRequests)

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/health", s.handleHealth)

		r.Get("/topics", s.handleListTopics)
		r.Post("/topics", s.handleCreateTopic)
		r.Get("/topics/{id}", s.handleGetTopic)
		r.Delete("/topics/{id}", s.handleDeleteTopic)
		r.Get("/topics/{id}/generation", s.handleGeneration)
		r.Post("/topics/{id}/regenerate", s.handleRegenerate)

		r.Post("/sessions", s.handleCreateSession)
		r.Get("/sessions/{id}", s.handleGetSession)
		r.Get("/sessions/{id}/next", s.handleNext)
		r.Post("/sessions/{id}/answer", s.handleAnswer)

		r.Post("/attempts/{id}/explain", s.handleExplain)

		r.Get("/stats", s.handleStats)

		r.Get("/settings", s.handleGetSettings)
		r.Put("/settings", s.handlePutSettings)

		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusNotFound, "no such endpoint")
		})
	})

	if web.Built() {
		r.Handle("/*", spaHandler(web.Assets()))
	} else {
		// The frontend is generated, not tracked, so a fresh clone has none.
		// Say so in the browser rather than serving a blank 404 — the API is
		// perfectly usable meanwhile.
		r.Handle("/*", http.HandlerFunc(unbuiltHandler))
	}
	return r
}

// spaHandler serves the built frontend, falling back to index.html so that
// client-side routes survive a reload or a deep link.
func spaHandler(assets fs.FS) http.Handler {
	files := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(assets, path); err != nil {
			// Unknown path: hand it to the SPA router rather than 404ing.
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}

// unbuiltPage explains how to produce the frontend that is missing.
const unbuiltPage = `<!doctype html>
<meta charset="utf-8">
<title>Trivial — frontend not built</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>
 body{font:16px/1.6 system-ui,sans-serif;max-width:34rem;margin:4rem auto;padding:0 1.5rem;
      background:#e7ebe3;color:#161c18}
 h1{font-size:1.5rem;margin:0 0 .5rem}
 pre{background:#dde3d7;padding:1rem;border-radius:4px;overflow-x:auto}
 code{font-family:ui-monospace,Consolas,monospace}
 a{color:#5b3df5}
 @media(prefers-color-scheme:dark){body{background:#14181a;color:#e3e9e1}
   pre{background:#1b2022}a{color:#9d8dff}}
</style>
<h1>The web interface has not been built</h1>
<p>Trivial's frontend is generated rather than checked in, so a fresh clone has
no UI embedded. From the project root:</p>
<pre><code>cd web
npm install
npm run build
cd ..
go build -o trivial ./cmd/trivial</code></pre>
<p>Then restart the server. The REST API is already running and works without
this — try <a href="/api/v1/health">/api/v1/health</a>.</p>
`

func unbuiltHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// 503: the server is fine, this one capability is not installed yet.
	w.WriteHeader(http.StatusServiceUnavailable)
	w.Write([]byte(unbuiltPage))
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only API traffic is worth logging; static assets would drown it out.
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		s.log.Info("request",
			"method", r.Method, "path", r.URL.Path,
			"status", ww.Status(), "ms", time.Since(start).Milliseconds())
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"provider": s.cfg.Provider,
		"model":    s.cfg.Model,
		"ui_built": web.Built(),
	})
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		if err := json.NewEncoder(w).Encode(v); err != nil {
			slog.Default().Error("write response", "err", err)
		}
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeStoreError maps store errors onto status codes.
func writeStoreError(w http.ResponseWriter, err error, what string) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, what+" not found")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
	return true
}

// decodeBody reads a JSON request body, rejecting unknown fields so typos in
// the client surface as errors rather than silently doing nothing.
func decodeBody(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// pathID reads a numeric URL parameter.
func pathID(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, name), 10, 64)
}
