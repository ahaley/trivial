package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ahaley/trivial/internal/model"
)

// ErrQueueEmpty means the session has no unanswered items left.
var ErrQueueEmpty = errors.New("session queue empty")

// Candidate is a fact considered for inclusion in a session, carrying
// everything the pedagogy engine needs to rank it and pick a question mode.
type Candidate struct {
	FactID     int64
	TopicID    int64
	Tags       []string
	Difficulty int

	// Review state; Reps == 0 means the fact has never been seen.
	Stability  float64
	FSRSDiff   float64
	DueAt      time.Time
	LastReview time.Time
	Reps       int
	Lapses     int

	// Modes maps an available question mode to its question ID.
	Modes map[model.Mode]int64
}

// New reports whether the fact has never been reviewed.
func (c Candidate) New() bool { return c.Reps == 0 }

// Candidates loads every answerable fact, optionally restricted to one topic
// and/or one subtopic tag. Only facts from ready topics that have at least one
// question rendering are returned.
//
// The whole candidate set is loaded and ranked in Go rather than in SQL: a
// personal deck is small, and the ranking (SPEC.md §7) is easier to read and
// test as ordinary code.
func (s *Store) Candidates(topicID *int64, tag string) ([]Candidate, error) {
	args := []any{string(model.StatusReady)}
	where := "t.status = ?"
	if topicID != nil {
		where += " AND f.topic_id = ?"
		args = append(args, *topicID)
	}

	rows, err := s.db.Query(`
		SELECT f.id, f.topic_id, f.tags, f.difficulty,
		       r.stability, r.difficulty, r.due_at, r.last_reviewed_at, r.reps, r.lapses
		FROM facts f
		JOIN topics t ON t.id = f.topic_id
		LEFT JOIN review_state r ON r.fact_id = f.id
		WHERE `+where+`
		ORDER BY f.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("load candidates: %w", err)
	}
	defer rows.Close()

	var out []Candidate
	byID := map[int64]int{}
	for rows.Next() {
		var c Candidate
		var tags string
		var stability, diff sql.NullFloat64
		var due, last sql.NullString
		var reps, lapses sql.NullInt64
		if err := rows.Scan(&c.FactID, &c.TopicID, &tags, &c.Difficulty,
			&stability, &diff, &due, &last, &reps, &lapses); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(tags), &c.Tags); err != nil {
			c.Tags = nil
		}
		if tag != "" && !hasTag(c.Tags, tag) {
			continue
		}
		c.Stability = stability.Float64
		c.FSRSDiff = diff.Float64
		c.DueAt = mustParseTime(due.String)
		c.LastReview = mustParseTime(last.String)
		c.Reps = int(reps.Int64)
		c.Lapses = int(lapses.Int64)
		c.Modes = map[model.Mode]int64{}
		byID[c.FactID] = len(out)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}

	qrows, err := s.db.Query(`SELECT fact_id, mode, id FROM questions`)
	if err != nil {
		return nil, fmt.Errorf("load candidate questions: %w", err)
	}
	defer qrows.Close()
	for qrows.Next() {
		var factID, qid int64
		var mode string
		if err := qrows.Scan(&factID, &mode, &qid); err != nil {
			return nil, err
		}
		if i, ok := byID[factID]; ok {
			out[i].Modes[model.Mode(mode)] = qid
		}
	}
	if err := qrows.Err(); err != nil {
		return nil, err
	}

	// A fact with no rendering cannot be asked.
	kept := out[:0]
	for _, c := range out {
		if len(c.Modes) > 0 {
			kept = append(kept, c)
		}
	}
	return kept, nil
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if strings.EqualFold(t, want) {
			return true
		}
	}
	return false
}

// QueueItem is one planned question in a session's queue.
type QueueItem struct {
	QuestionID int64
	FactID     int64
}

// CreateSession writes a session and its composed queue.
func (s *Store) CreateSession(topicID *int64, kind model.SessionKind, tag string, items []QueueItem) (model.Session, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return model.Session{}, err
	}
	defer tx.Rollback()

	now := time.Now()
	res, err := tx.Exec(
		`INSERT INTO sessions (topic_id, kind, tag, planned, started_at) VALUES (?, ?, ?, ?, ?)`,
		topicID, string(kind), tag, len(items), formatTime(now))
	if err != nil {
		return model.Session{}, fmt.Errorf("insert session: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Session{}, err
	}
	for i, it := range items {
		if _, err := tx.Exec(
			`INSERT INTO session_items (session_id, position, question_id, fact_id) VALUES (?, ?, ?, ?)`,
			id, i, it.QuestionID, it.FactID); err != nil {
			return model.Session{}, fmt.Errorf("insert session item: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return model.Session{}, err
	}
	return model.Session{
		ID: id, TopicID: topicID, Kind: kind, Tag: tag,
		Planned: len(items), StartedAt: now,
	}, nil
}

// GetSession returns a session by ID.
func (s *Store) GetSession(id int64) (model.Session, error) {
	var sess model.Session
	var topicID sql.NullInt64
	var kind, started string
	var finished sql.NullString
	err := s.db.QueryRow(
		`SELECT id, topic_id, kind, tag, planned, started_at, finished_at FROM sessions WHERE id = ?`, id).
		Scan(&sess.ID, &topicID, &kind, &sess.Tag, &sess.Planned, &started, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Session{}, ErrNotFound
	}
	if err != nil {
		return model.Session{}, fmt.Errorf("get session: %w", err)
	}
	if topicID.Valid {
		sess.TopicID = &topicID.Int64
	}
	sess.Kind = model.SessionKind(kind)
	sess.StartedAt = mustParseTime(started)
	if finished.Valid {
		t := mustParseTime(finished.String)
		sess.FinishedAt = &t
	}
	return sess, nil
}

// NextItem returns the session's next unanswered queue item together with its
// question and fact. It returns ErrQueueEmpty when the session is finished.
func (s *Store) NextItem(sessionID int64) (itemID int64, q model.Question, f model.Fact, err error) {
	var tags string
	var mode, options string
	row := s.db.QueryRow(`
		SELECT si.id,
		       q.id, q.fact_id, q.mode, q.prompt, q.options, q.correct_option,
		       f.id, f.topic_id, f.statement, f.canonical_answer, f.explanation, f.tags, f.difficulty
		FROM session_items si
		JOIN questions q ON q.id = si.question_id
		JOIN facts f ON f.id = si.fact_id
		WHERE si.session_id = ? AND si.answered = 0
		ORDER BY si.position LIMIT 1`, sessionID)

	err = row.Scan(&itemID,
		&q.ID, &q.FactID, &mode, &q.Prompt, &options, &q.CorrectOption,
		&f.ID, &f.TopicID, &f.Statement, &f.CanonicalAnswer, &f.Explanation, &tags, &f.Difficulty)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, q, f, ErrQueueEmpty
	}
	if err != nil {
		return 0, q, f, fmt.Errorf("next item: %w", err)
	}
	q.Mode = model.Mode(mode)
	if err := json.Unmarshal([]byte(options), &q.Options); err != nil {
		q.Options = nil
	}
	if err := json.Unmarshal([]byte(tags), &f.Tags); err != nil {
		f.Tags = nil
	}
	return itemID, q, f, nil
}

// MarkAnswered flags a queue item as done.
func (s *Store) MarkAnswered(itemID int64) error {
	_, err := s.db.Exec(`UPDATE session_items SET answered = 1 WHERE id = ?`, itemID)
	return err
}

// MaxRelearnPerFact bounds how often one fact may be re-queued within a single
// session. Without a bound, a fact the learner keeps missing would re-queue
// itself forever and the session could never end. One repeat is enough to
// reinforce the correction; a fact still missed after that is already scheduled
// minutes away by FSRS, so the next session picks it straight back up.
const MaxRelearnPerFact = 1

// AppendRelearn adds a repeat of a fact to the end of a session's queue, so a
// miss is re-tested before the user leaves (SPEC.md §7, immediate reinforcement).
//
// It reports whether a repeat was actually queued. Nothing is queued if the
// fact is already waiting in the queue or has already used up its repeats.
func (s *Store) AppendRelearn(sessionID, questionID, factID int64) (bool, error) {
	var pending, repeats int
	if err := s.db.QueryRow(`
		SELECT COALESCE(SUM(answered = 0), 0), COALESCE(SUM(is_relearn), 0)
		FROM session_items WHERE session_id = ? AND fact_id = ?`,
		sessionID, factID).Scan(&pending, &repeats); err != nil {
		return false, err
	}
	if pending > 0 || repeats >= MaxRelearnPerFact {
		return false, nil
	}

	var maxPos sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(position) FROM session_items WHERE session_id = ?`, sessionID).
		Scan(&maxPos); err != nil {
		return false, err
	}
	if _, err := s.db.Exec(`
		INSERT INTO session_items (session_id, position, question_id, fact_id, is_relearn)
		VALUES (?, ?, ?, ?, 1)`, sessionID, maxPos.Int64+1, questionID, factID); err != nil {
		return false, err
	}
	return true, nil
}

// QuestionForFact returns the fact's question in the given mode, if it exists.
func (s *Store) QuestionForFact(factID int64, mode model.Mode) (model.Question, error) {
	q, err := scanQuestion(s.db.QueryRow(
		`SELECT id, fact_id, mode, prompt, options, correct_option FROM questions WHERE fact_id = ? AND mode = ?`,
		factID, string(mode)))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Question{}, ErrNotFound
	}
	return q, err
}

// Progress reports how many of a session's queued items have been answered.
func (s *Store) Progress(sessionID int64) (answered, total int, err error) {
	err = s.db.QueryRow(
		`SELECT COALESCE(SUM(answered), 0), COUNT(*) FROM session_items WHERE session_id = ?`, sessionID).
		Scan(&answered, &total)
	return answered, total, err
}

// RecordAttempt saves an answer and returns its ID.
func (s *Store) RecordAttempt(a model.Attempt) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO attempts (session_id, question_id, fact_id, response, grade, rating, latency_ms, critique, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.SessionID, a.QuestionID, a.FactID, a.Response, string(a.Grade), a.Rating,
		a.LatencyMS, a.Critique, formatTime(time.Now()))
	if err != nil {
		return 0, fmt.Errorf("record attempt: %w", err)
	}
	return res.LastInsertId()
}

// AttemptContext is an attempt together with the fact it was about — what the
// "explain more" endpoint needs to prompt the model.
type AttemptContext struct {
	Attempt model.Attempt
	Fact    model.Fact
	Subject string
}

// GetAttemptContext loads an attempt with its fact and topic subject.
func (s *Store) GetAttemptContext(id int64) (AttemptContext, error) {
	var ac AttemptContext
	var grade, created, tags string
	err := s.db.QueryRow(`
		SELECT a.id, a.session_id, a.question_id, a.fact_id, a.response, a.grade, a.rating,
		       a.latency_ms, a.critique, a.elaboration, a.created_at,
		       f.id, f.topic_id, f.statement, f.canonical_answer, f.explanation, f.tags, f.difficulty,
		       t.subject
		FROM attempts a
		JOIN facts f ON f.id = a.fact_id
		JOIN topics t ON t.id = f.topic_id
		WHERE a.id = ?`, id).
		Scan(&ac.Attempt.ID, &ac.Attempt.SessionID, &ac.Attempt.QuestionID, &ac.Attempt.FactID,
			&ac.Attempt.Response, &grade, &ac.Attempt.Rating, &ac.Attempt.LatencyMS,
			&ac.Attempt.Critique, &ac.Attempt.Elaboration, &created,
			&ac.Fact.ID, &ac.Fact.TopicID, &ac.Fact.Statement, &ac.Fact.CanonicalAnswer,
			&ac.Fact.Explanation, &tags, &ac.Fact.Difficulty, &ac.Subject)
	if errors.Is(err, sql.ErrNoRows) {
		return ac, ErrNotFound
	}
	if err != nil {
		return ac, fmt.Errorf("get attempt: %w", err)
	}
	ac.Attempt.Grade = model.Grade(grade)
	ac.Attempt.CreatedAt = mustParseTime(created)
	if err := json.Unmarshal([]byte(tags), &ac.Fact.Tags); err != nil {
		ac.Fact.Tags = nil
	}
	return ac, nil
}

// SaveElaboration caches an "explain more" response on the attempt.
func (s *Store) SaveElaboration(attemptID int64, text string) error {
	_, err := s.db.Exec(`UPDATE attempts SET elaboration = ? WHERE id = ?`, text, attemptID)
	return err
}

// SaveReview upserts a fact's FSRS scheduling state.
func (s *Store) SaveReview(r model.Review) error {
	_, err := s.db.Exec(`
		INSERT INTO review_state (fact_id, stability, difficulty, due_at, last_reviewed_at, reps, lapses)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(fact_id) DO UPDATE SET
			stability = excluded.stability,
			difficulty = excluded.difficulty,
			due_at = excluded.due_at,
			last_reviewed_at = excluded.last_reviewed_at,
			reps = excluded.reps,
			lapses = excluded.lapses`,
		r.FactID, r.Stability, r.Difficulty, formatTime(r.DueAt), formatTime(r.LastReview), r.Reps, r.Lapses)
	if err != nil {
		return fmt.Errorf("save review state: %w", err)
	}
	return nil
}

// GetReview returns a fact's scheduling state, or a zero Review if unseen.
func (s *Store) GetReview(factID int64) (model.Review, error) {
	r, err := scanReview(s.db.QueryRow(
		`SELECT fact_id, stability, difficulty, due_at, last_reviewed_at, reps, lapses
		 FROM review_state WHERE fact_id = ?`, factID))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Review{FactID: factID}, nil
	}
	if err != nil {
		return model.Review{}, fmt.Errorf("get review state: %w", err)
	}
	return r, nil
}

// FinishSession stamps the session's completion time (idempotent).
func (s *Store) FinishSession(id int64) error {
	_, err := s.db.Exec(
		`UPDATE sessions SET finished_at = ? WHERE id = ? AND finished_at IS NULL`,
		formatTime(time.Now()), id)
	return err
}

// TagScore is a subtopic's accuracy over recent attempts.
type TagScore struct {
	Tag      string  `json:"tag"`
	Attempts int     `json:"attempts"`
	Correct  int     `json:"correct"`
	Accuracy float64 `json:"accuracy"`
}

// SessionResult is the end-of-session summary (SPEC.md §4.2).
type SessionResult struct {
	Session   model.Session `json:"session"`
	Answered  int           `json:"answered"`
	Correct   int           `json:"correct"`
	Partial   int           `json:"partial"`
	Incorrect int           `json:"incorrect"`
	// WeakTags are the session's worst-performing subtopics.
	WeakTags []TagScore `json:"weak_tags"`
	// Upcoming lists what this session scheduled, soonest first.
	Upcoming []ScheduledFact `json:"upcoming"`
}

// ScheduledFact is a fact and when it next comes back.
type ScheduledFact struct {
	FactID    int64     `json:"fact_id"`
	Statement string    `json:"statement"`
	DueAt     time.Time `json:"due_at"`
	Stability float64   `json:"stability"`
}

// SessionSummary computes a finished session's results.
func (s *Store) SessionSummary(id int64) (SessionResult, error) {
	sess, err := s.GetSession(id)
	if err != nil {
		return SessionResult{}, err
	}
	res := SessionResult{Session: sess}

	rows, err := s.db.Query(`
		SELECT a.grade, f.tags FROM attempts a JOIN facts f ON f.id = a.fact_id
		WHERE a.session_id = ?`, id)
	if err != nil {
		return res, fmt.Errorf("session summary: %w", err)
	}
	defer rows.Close()

	tally := map[string]*TagScore{}
	for rows.Next() {
		var grade, tags string
		if err := rows.Scan(&grade, &tags); err != nil {
			return res, err
		}
		res.Answered++
		switch model.Grade(grade) {
		case model.GradeCorrect:
			res.Correct++
		case model.GradePartial:
			res.Partial++
		default:
			res.Incorrect++
		}
		var parsed []string
		json.Unmarshal([]byte(tags), &parsed)
		for _, t := range parsed {
			ts, ok := tally[t]
			if !ok {
				ts = &TagScore{Tag: t}
				tally[t] = ts
			}
			ts.Attempts++
			if model.Grade(grade) == model.GradeCorrect {
				ts.Correct++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return res, err
	}
	res.WeakTags = rankWeakest(tally, 5)

	urows, err := s.db.Query(`
		SELECT DISTINCT f.id, f.statement, r.due_at, r.stability
		FROM attempts a
		JOIN facts f ON f.id = a.fact_id
		JOIN review_state r ON r.fact_id = f.id
		WHERE a.session_id = ?
		ORDER BY r.due_at LIMIT 20`, id)
	if err != nil {
		return res, fmt.Errorf("session schedule: %w", err)
	}
	defer urows.Close()
	for urows.Next() {
		var sf ScheduledFact
		var due string
		if err := urows.Scan(&sf.FactID, &sf.Statement, &due, &sf.Stability); err != nil {
			return res, err
		}
		sf.DueAt = mustParseTime(due)
		res.Upcoming = append(res.Upcoming, sf)
	}
	return res, urows.Err()
}
