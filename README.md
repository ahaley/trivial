# Trivial

A self-hosted trivia trainer with spaced repetition. Name a subject; an LLM
researches it and writes a deck of questions; you quiz yourself. Every answer
is recorded, and an [FSRS-5](https://github.com/open-spaced-repetition/fsrs4anki)
scheduler brings back what you keep getting wrong, right before you'd forget it.

- **Single binary, single SQLite file, no accounts.** The React frontend is
  embedded in the Go binary; there is nothing else to deploy.
- **Generated decks.** Point it at any subject — "The Roman Republic",
  "PostgreSQL internals" — and it builds a fact deck with both open-ended and
  multiple-choice renderings of every fact.
- **Real pedagogy.** Recall before recognition, immediate correction with
  on-demand elaboration, same-session reinforcement of misses, and interleaved
  review across topics. The forgetting curves drawn in the UI are the actual
  curves the scheduler plans against.
- **Pluggable LLM backends.** Vertex AI (service account) by default, Google AI
  Studio (API key) as an alternative, and an offline mock for trying the app
  without any credentials.

See [SPEC.md](SPEC.md) for the full design.

## Quick start

Prerequisites: Go 1.26+, and Node.js only if you change the frontend (a built
copy is committed, so a plain `go build` works from a fresh clone).

```sh
git clone https://github.com/ahaley/trivial
cd trivial
go build -o trivial ./cmd/trivial

# Point it at a Google Cloud service account and run
export TRIVIAL_VERTEX_CREDENTIALS=/path/to/service-account.json
./trivial serve --host localhost:1234
```

Open <http://localhost:1234>, create a topic, and start a session.

**No credentials to hand?** Run with `--llm-provider mock` to explore the whole
app offline against a synthetic deck. The content is obvious filler, but
generation, quizzing, grading and scheduling all work.

By default Trivial talks to **Vertex AI** at the `global` endpoint using
`gemini-3.5-flash`. Nothing else needs configuring.

## Commands

```
trivial serve --host localhost:1234     Start the web application
trivial generate "The Roman Republic"   Build a deck from the terminal
trivial topics                          List decks and how well you know them
trivial stats                           Due queue and weakest subtopics
trivial config                          Show where each setting came from
trivial models                          Show which models your project can use
```

`generate` takes `--count` and `--guidance`. Every command takes `--db`,
`--llm-provider` and `--llm-model`.

## Configuration

Highest precedence first: flags, environment, config file, defaults.

| Setting | Environment | Default |
|---|---|---|
| Listen address | `TRIVIAL_HOST` | `localhost:1234` |
| Database | `TRIVIAL_DB_PATH` | `<user config dir>/trivial/trivial.db` |
| Provider | `TRIVIAL_LLM_PROVIDER` | `vertex` (or `gemini`, `mock`) |
| Model | `TRIVIAL_LLM_MODEL` | `gemini-3.5-flash` |
| Credentials | `TRIVIAL_VERTEX_CREDENTIALS` | application default credentials |
| Project | `TRIVIAL_VERTEX_PROJECT` | from the credentials |
| Region | `TRIVIAL_VERTEX_LOCATION` | `global` |
| API key (`gemini` only) | `TRIVIAL_LLM_API_KEY` | — |

The config file lives at `<user config dir>/trivial/config.toml`, or wherever
`--config` points:

```toml
host = "localhost:1234"

llm_model = "gemini-3.5-flash"
vertex_credentials = "/path/to/service-account.json"
# vertex_project  = "my-project"    # default: whatever the credentials name
# vertex_location = "global"

target_count = 30    # facts per deck
session_size = 20    # questions per session
```

`trivial config` prints the resolved values, which is the fastest way to find
out why a setting is not what you expected.

### Credentials

`TRIVIAL_VERTEX_CREDENTIALS` (or `--vertex-credentials`) points Trivial at its
own service account without touching `GOOGLE_APPLICATION_CREDENTIALS`, which is
process-wide and may already be set for something else on the machine. Where
both are set, Trivial's wins.

Leave it unset and credentials resolve in the usual Application Default
Credentials order instead, so `GOOGLE_APPLICATION_CREDENTIALS`,
`gcloud auth application-default login`, and an attached GCE/Cloud Run identity
all work. Access tokens are refreshed automatically either way.

The project is taken from the credentials; override with `--vertex-project` or
`TRIVIAL_VERTEX_PROJECT`. The service account needs the **Vertex AI User** role
(`roles/aiplatform.user`), and `aiplatform.googleapis.com` must be enabled.

`trivial config --llm-provider vertex` prints the resolved project, region and
credential source, which is the fastest way to diagnose a setup problem without
making a billed request.

### When a model 404s

Model availability varies by region, and Vertex model IDs do not always match
AI Studio's. A 404 means the credentials were accepted and the *model* was not.

```sh
trivial models                          # ask the project what it offers
trivial models gemini-3.5-flash         # confirm a specific ID answers
```

Set the winner with `TRIVIAL_LLM_MODEL`, or per-run with `--llm-model`. If a
named region is in play, try `--vertex-location global` first — that is where
Gemini models appear most reliably, and it is the default here for that reason.

Listing needs its own IAM permission; where a service account lacks it, `models`
falls back to trying candidate IDs directly, which is the better test anyway —
a model can be listed and still be unusable from your project.

### Other providers

`--llm-provider gemini` reaches the same models through Google AI Studio with a
plain `TRIVIAL_LLM_API_KEY` instead of a service account. The provider
interface is deliberately small, so further backends slot in behind it —
contributions welcome.

## How the scheduling works

Each fact carries [FSRS-5](https://github.com/open-spaced-repetition/fsrs4anki)
state: a *stability* in days and a *difficulty* on 1–10. After every answer both
are updated, and the fact's next due date is the point where its predicted
recall falls to 90%.

On top of that, four things shape what you actually see:

- **Recall before recognition.** A fact you do not know yet is asked
  open-ended; once it is secure it graduates to a faster multiple-choice check.
  That is why generation writes both renderings for every fact.
- **Immediate correction.** A miss shows the canonical answer and a specific
  correction straight away, and **Explain more** goes back to the model for a
  fuller explanation on demand.
- **Same-session reinforcement.** A missed fact is re-queued once, a few
  questions later, so you get a second real retrieval attempt before you leave.
- **Interleaving.** Review sessions mix topics and subtopics rather than
  blocking by category.

## Access from your other devices

Trivial has no authentication, by design. To use it from a phone or laptop, put
it on a private network such as a [Tailscale](https://tailscale.com/) tailnet
rather than opening a port.

Leave Trivial bound to loopback — the default — and let Tailscale proxy to it:

```sh
trivial serve                      # stays on localhost:1234
tailscale serve --bg 1234          # https://<machine>.<tailnet>.ts.net
```

Your other devices then reach it over HTTPS with a real certificate, and the
listener is never exposed on your LAN. Do **not** bind `0.0.0.0` for this;
`tailscale serve` connects to 127.0.0.1, and binding every interface only widens
the exposure.

If something else already occupies the tailnet root — check with
`tailscale serve status` — give Trivial its own HTTPS port instead of
displacing it:

```sh
tailscale serve --bg --https=8443 1234
```

Mounting under a path (`--set-path=/trivial`) will not work: the frontend
requests its assets and API from absolute paths, so it would need rebuilding
with a matching Vite `base`.

To stop sharing: `tailscale serve --https=443 off` (or whichever port you used).

**Do not use `tailscale funnel`** — that publishes to the open internet, and
anyone reaching Trivial can read and write your data and spend your LLM budget.

## Development

```sh
go test ./...                  # backend tests
cd web && npm install          # once
cd web && npm run dev          # Vite on :5173, proxying /api to :1234
cd web && npm run typecheck
```

Run `trivial serve` alongside `npm run dev` for hot reload; set
`TRIVIAL_DEV_API` if the backend is not on `localhost:1234`. The Go binary
serves the *embedded* build, so after a frontend change re-run
`npm run build` (and commit `web/dist` — it is what makes `go build` work
without Node) and then `go build`.

### Layout

```
cmd/trivial          entry point
internal/config      flags > env > file > defaults
internal/store       SQLite, schema and queries
internal/model       domain types
internal/llm         provider interface; AI Studio, Vertex AI and offline mock
internal/generate    research → render → validate pipeline
internal/tutor       answer grading and elaboration
internal/fsrs        FSRS-5 scheduler
internal/pedagogy    session composition and question-mode choice
internal/server      REST API and SPA hosting
web/                 React + TypeScript frontend, embedded at build time
```

## Contributing

Issues and pull requests are welcome. For anything larger than a bug fix,
please open an issue first to discuss the approach. Run `go test ./...` and
`npm run typecheck` before submitting, and rebuild `web/dist` if you touched
the frontend.

## License

[MIT](LICENSE)
