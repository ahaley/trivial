-- Initial schema (SPEC.md §10).

CREATE TABLE topics (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    subject        TEXT    NOT NULL,
    guidance       TEXT    NOT NULL DEFAULT '',
    status         TEXT    NOT NULL CHECK (status IN ('generating','ready','failed')),
    target_count   INTEGER NOT NULL,
    stage          TEXT    NOT NULL DEFAULT '',
    progress       INTEGER NOT NULL DEFAULT 0,
    error          TEXT    NOT NULL DEFAULT '',
    created_at     TEXT    NOT NULL,
    updated_at     TEXT    NOT NULL
);

CREATE TABLE facts (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    topic_id         INTEGER NOT NULL REFERENCES topics(id) ON DELETE CASCADE,
    statement        TEXT    NOT NULL,
    canonical_answer TEXT    NOT NULL,
    explanation      TEXT    NOT NULL DEFAULT '',
    tags             TEXT    NOT NULL DEFAULT '[]',
    difficulty       INTEGER NOT NULL DEFAULT 3,
    -- Normalised form of the statement; the unique index below is what makes
    -- near-duplicate facts impossible to insert twice into one topic.
    norm_key         TEXT    NOT NULL,
    created_at       TEXT    NOT NULL
);

CREATE UNIQUE INDEX idx_facts_dedup ON facts(topic_id, norm_key);
CREATE INDEX idx_facts_topic ON facts(topic_id);

CREATE TABLE questions (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    fact_id        INTEGER NOT NULL REFERENCES facts(id) ON DELETE CASCADE,
    mode           TEXT    NOT NULL CHECK (mode IN ('open','mc')),
    prompt         TEXT    NOT NULL,
    options        TEXT    NOT NULL DEFAULT '[]',
    correct_option INTEGER NOT NULL DEFAULT -1,
    created_at     TEXT    NOT NULL
);

-- A fact carries at most one rendering per mode.
CREATE UNIQUE INDEX idx_questions_fact_mode ON questions(fact_id, mode);

CREATE TABLE sessions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    topic_id    INTEGER REFERENCES topics(id) ON DELETE CASCADE,
    kind        TEXT    NOT NULL CHECK (kind IN ('topic','review','drill')),
    tag         TEXT    NOT NULL DEFAULT '',
    planned     INTEGER NOT NULL DEFAULT 0,
    started_at  TEXT    NOT NULL,
    finished_at TEXT
);

-- The composed queue for a session. Relearning re-asks are appended here
-- mid-session, which is why the queue is a table rather than a computed order.
CREATE TABLE session_items (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    position    INTEGER NOT NULL,
    question_id INTEGER NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
    fact_id     INTEGER NOT NULL REFERENCES facts(id) ON DELETE CASCADE,
    answered    INTEGER NOT NULL DEFAULT 0,
    is_relearn  INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_session_items_queue ON session_items(session_id, answered, position);

CREATE TABLE attempts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id  INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    question_id INTEGER NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
    fact_id     INTEGER NOT NULL REFERENCES facts(id) ON DELETE CASCADE,
    response    TEXT    NOT NULL DEFAULT '',
    grade       TEXT    NOT NULL CHECK (grade IN ('correct','partial','incorrect')),
    rating      INTEGER NOT NULL,
    latency_ms  INTEGER NOT NULL DEFAULT 0,
    critique    TEXT    NOT NULL DEFAULT '',
    elaboration TEXT    NOT NULL DEFAULT '',
    created_at  TEXT    NOT NULL
);

CREATE INDEX idx_attempts_session ON attempts(session_id);
CREATE INDEX idx_attempts_fact ON attempts(fact_id, created_at);

CREATE TABLE review_state (
    fact_id          INTEGER PRIMARY KEY REFERENCES facts(id) ON DELETE CASCADE,
    stability        REAL    NOT NULL,
    difficulty       REAL    NOT NULL,
    due_at           TEXT    NOT NULL,
    last_reviewed_at TEXT    NOT NULL,
    reps             INTEGER NOT NULL DEFAULT 0,
    lapses           INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_review_due ON review_state(due_at);
