# Trivial — Specification

## 1. Overview

**Trivial** is a personal trivia application that works out the user's trivia muscle. The user names a subject; an LLM researches it and builds a corpus of trivia questions; the user quizzes themselves through the web UI. Every answer is recorded, weaknesses are discovered, and a spaced-repetition pedagogy engine schedules weak material for reinforcement — turning casual trivia into deliberate self-improvement.

It is a single-user tool: it runs locally as one binary, and can optionally be exposed across the user's own devices over a personal Tailscale tailnet. There are no accounts, profiles, or auth.

## 2. Goals

- Generate high-quality trivia decks on any subject via an LLM, with clear, unambiguous questions and answers.
- Support two question modes — **open-ended** and **multiple choice** — with the same underlying fact optionally askable in both modes.
- Record every attempt and build a per-fact mastery model.
- Use a leading-edge, research-backed pedagogy (FSRS spaced repetition + retrieval practice) to resurface weak areas and correct misunderstandings.
- Offer on-demand deeper context ("explain more") from the LLM when an answer or explanation isn't enough.
- Ship as a single Go binary with an embedded React frontend and a SQLite database — trivially easy to run.

### Non-goals

- Multi-user accounts, profiles, or authentication.
- Public internet hosting; anything beyond localhost + personal tailnet.
- Native mobile apps (the web UI should simply work well on a phone browser over the tailnet).
- Offline/local LLM inference.
- Real-time multiplayer or competitive play.

## 3. Core concepts

| Concept | Meaning |
|---|---|
| **Topic** | A user-created subject (e.g. "The Roman Republic", "Grand Slam tennis"). Owns a deck of facts. |
| **Fact** | A canonical unit of knowledge within a topic: statement, canonical answer, explanation, and subtopic tags. The atom that mastery is tracked against. |
| **Question** | A rendering of a fact in one of two modes: **open-ended** (free-text answer) or **multiple choice** (1 correct + 3 plausible distractors). A fact may have one or both renderings. |
| **Session** | One quiz run: an ordered sequence of questions selected by the scheduler, answered with immediate feedback. |
| **Attempt** | A recorded answer to a question within a session: the response, grade, timing, and any requested elaboration. |
| **Review state** | Per-fact FSRS scheduling state (stability, difficulty, due date) driving when the fact is next asked. |

## 4. User experience

An expertly designed, fast, keyboard-friendly site with three main surfaces:

### 4.1 Create a topic

1. User enters a subject and optional guidance (angle, difficulty, question count).
2. Backend kicks off generation; the UI shows live progress (researching → drafting facts → rendering questions → validating).
3. On completion the topic page shows the deck: fact count, mode mix, subtopic breakdown, and a **Start session** button.

### 4.2 Quiz session

1. User starts a session (per topic, or a mixed "due for review" session across topics).
2. Questions are presented one at a time:
   - **Multiple choice**: four options, selectable by click or keys 1–4.
   - **Open-ended**: free-text input, submitted with Enter.
3. Immediate feedback after each answer: correct/partially correct/incorrect, the canonical answer, and a short explanation.
4. On a wrong or partial answer, the feedback leads with a correction of the specific misunderstanding.
5. An **Explain more** button is always available in feedback; it calls the LLM for a deeper, conversational elaboration of the fact in context.
6. Session ends with a summary: score, weakest subtopics, and what's newly scheduled for review.

### 4.3 Review dashboard

- Mastery over time, per topic and overall.
- Weak-area clusters (subtopic tags ranked by error rate).
- The due queue: which facts are due for review and when, with a one-click "review due now" session.

## 5. Question generation

Generation is an LLM pipeline run by the Go backend:

1. **Research** — the model is prompted to survey the subject and produce a structured corpus of canonical facts: statement, canonical answer, explanation, subtopic tags, difficulty (1–5).
2. **Render** — each fact is rendered into questions: an open-ended phrasing and/or a multiple-choice phrasing with three plausible, non-trivial distractors. The same fact may get both renderings; mode choice is later driven by the pedagogy engine.
3. **Validate** — outputs must conform to a JSON schema (enforced via structured-output constraints and backend validation). Malformed items are retried; near-duplicate facts (normalized-text similarity) are dropped.

Quality bar: questions and answers must be **clear** — unambiguous phrasing, a single defensible canonical answer, distractors that are wrong but plausible.

- Default model: `gemini-3.6-flash` (fast and cheap — generation is bursty).
- Model ID, API key, and endpoint are configurable via config file / environment (`TRIVIAL_LLM_MODEL`, `TRIVIAL_LLM_API_KEY`); the LLM client is a small interface so providers can be swapped later.

## 6. Answer evaluation

- **Multiple choice** — checked locally by option ID. Instant.
- **Open-ended** — graded by the LLM against the fact's canonical answer and explanation. The grader returns a structured verdict: `correct` / `partially_correct` / `incorrect`, plus a one-to-two-sentence critique naming exactly what was missing or mistaken. Lenient on spelling and phrasing, strict on substance.
- Grades map to FSRS ratings: incorrect → *Again*, partially correct → *Hard*, correct → *Good* (a fast, confident correct answer may map to *Easy* using response time).

## 7. Pedagogy engine

The "leading-edge" learning algorithm is a composition of well-evidenced techniques:

1. **FSRS scheduling** — each fact carries FSRS (Free Spaced Repetition Scheduler) state: stability, difficulty, and a due date updated after every attempt. FSRS is the current state of the art in open spaced-repetition scheduling and is well specified for implementation in Go.
2. **Retrieval practice with desirable difficulty** — recall beats recognition. New or shaky facts (low stability) are preferentially asked **open-ended**; well-known facts may be asked as faster multiple-choice checks. This is why one fact can carry both renderings.
3. **Immediate corrective feedback** — every miss is answered with the canonical answer and a targeted correction of the misunderstanding, reinforcing the correct model at the moment of error. **Explain more** escalates to a fuller LLM elaboration on demand.
4. **Interleaving** — mixed "due review" sessions interleave facts across topics and subtopics rather than blocking by category, which improves discrimination and retention.
5. **Weakness clustering** — error rates are aggregated by subtopic tag; the dashboard surfaces weak clusters and offers targeted mini-sessions ("drill: naval battles").

Session composition: due reviews first (FSRS queue), then new/unseen facts, interleaved; capped at a configurable session length (default 20 questions).

## 8. Architecture

```
┌────────────────────────────── trivial (single Go binary) ─┐
│  CLI (spf13/cobra)                                        │
│  HTTP server (net/http + chi)                             │
│   ├── REST JSON API  (/api/v1/...)                        │
│   ├── Embedded React build (go:embed, SPA fallback)       │
│   ├── Generation pipeline ──► LLM client (Gemini API)     │
│   ├── Grader ──────────────► LLM client                   │
│   └── Pedagogy engine (FSRS scheduler, session composer)  │
│  SQLite (modernc.org/sqlite — pure Go, CGO-free)          │
└───────────────────────────────────────────────────────────┘
```

- **Backend**: Go. Router: `chi`. DB: SQLite via `modernc.org/sqlite` (no CGO → painless cross-compilation). Migrations embedded and run on startup.
- **Frontend**: React + TypeScript + Vite. Production build embedded into the binary with `go:embed`; dev mode proxies Vite to the Go API.
- **Generation** runs as a background job; the UI polls job status (simple polling is sufficient at this scale).

## 9. API sketch

| Method & path | Purpose |
|---|---|
| `GET  /api/v1/topics` | List topics with deck/mastery summaries |
| `POST /api/v1/topics` | Create topic `{subject, guidance?, target_count?}`; starts generation |
| `GET  /api/v1/topics/{id}` | Topic detail: facts, mode mix, subtopics, mastery |
| `DELETE /api/v1/topics/{id}` | Delete topic and its history |
| `GET  /api/v1/topics/{id}/generation` | Generation job status/progress |
| `POST /api/v1/sessions` | Start a session `{topic_id?  // omit for cross-topic due review}` |
| `GET  /api/v1/sessions/{id}/next` | Next scheduled question (or session summary when done) |
| `POST /api/v1/sessions/{id}/answer` | Submit answer; returns grade, canonical answer, explanation |
| `POST /api/v1/attempts/{id}/explain` | "Explain more": LLM elaboration for that attempt's fact |
| `GET  /api/v1/stats` | Dashboard data: mastery curves, weak clusters, due queue |

## 10. Data model

SQLite tables (abridged; all have `id` and timestamps):

- **topics** — `subject`, `guidance`, `status` (generating | ready | failed)
- **facts** — `topic_id`, `statement`, `canonical_answer`, `explanation`, `tags` (JSON), `difficulty`
- **questions** — `fact_id`, `mode` (open | mc), `prompt`, `options` (JSON, MC only), `correct_option`
- **sessions** — `topic_id?`, `started_at`, `finished_at`, `kind` (topic | review | drill)
- **attempts** — `session_id`, `question_id`, `response`, `grade` (correct | partial | incorrect), `latency_ms`, `critique`
- **review_state** — `fact_id` (unique), FSRS fields: `stability`, `difficulty`, `due_at`, `last_reviewed_at`, `reps`, `lapses`

## 11. CLI

Single binary, subcommand style:

```
trivial serve --host localhost:1234     # start the web app (the raw spec's primary flow)
trivial generate "The Roman Republic"   # create a topic + deck from the terminal
trivial topics                          # list topics and mastery
trivial stats                           # due queue + weak areas in the terminal
```

Configuration precedence: flags > environment (`TRIVIAL_LLM_API_KEY`, `TRIVIAL_LLM_MODEL`, `TRIVIAL_DB_PATH`) > config file (`~/.config/trivial/config.toml`) > defaults. Default DB path: `~/.local/share/trivial/trivial.db` (platform-appropriate via `os.UserConfigDir`/`os.UserCacheDir` conventions).

## 12. Deployment

- **Local**: `trivial serve --host localhost:1234`. One process, one file of state (SQLite).
- **Tailnet**: expose the same server across personal devices with `tailscale serve` (or bind to the tailnet IP). Tailscale provides transport security and device identity, which is why the app itself needs no auth.

## 13. Milestones

1. **M1 — Skeleton**: cobra CLI, `serve` command, chi server, SQLite + migrations, embedded placeholder UI.
2. **M2 — Generation**: Gemini client, research→render→validate pipeline, topic creation UI with progress.
3. **M3 — Quiz loop**: sessions, MC + open-ended answering, LLM grading, immediate feedback, Explain more.
4. **M4 — Pedagogy**: FSRS state + scheduling, mode selection by stability, interleaved due-review sessions.
5. **M5 — Dashboard & polish**: stats endpoints, mastery/weakness dashboard, terminal `stats`, design polish.

## 14. Open questions

- Default deck size per topic (working assumption: ~30 facts, user-overridable at creation).
- Should generation support incremental "add 20 more questions" to an existing topic?
- Image-based questions (maps, artwork) — out of scope for now?
- FSRS parameter optimization from the user's own history (FSRS supports it) — later milestone or never?
