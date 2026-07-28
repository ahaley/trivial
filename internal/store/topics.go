package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/ahaley/trivial/internal/fsrs"
	"github.com/ahaley/trivial/internal/model"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// ErrBusy is returned when a topic is already generating.
var ErrBusy = errors.New("already generating")

// TopicSummary is a topic plus the aggregate numbers the UI lists it with.
type TopicSummary struct {
	model.Topic
	FactCount int `json:"fact_count"`
	DueCount  int `json:"due_count"`
	NewCount  int `json:"new_count"`
	// Mastery is mean predicted recall across the deck, right now.
	Mastery float64 `json:"mastery"`
	// Stability is mean FSRS stability in days across the facts that have been
	// seen at least once. It is what the deck's forgetting curve is drawn from.
	Stability float64 `json:"stability"`
}

// CreateTopic inserts a topic in the "generating" state and returns it.
func (s *Store) CreateTopic(subject, guidance string, targetCount int) (model.Topic, error) {
	now := time.Now()
	res, err := s.db.Exec(
		`INSERT INTO topics (subject, guidance, status, target_count, stage, progress, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 0, ?, ?)`,
		subject, guidance, string(model.StatusGenerating), targetCount, "queued",
		formatTime(now), formatTime(now))
	if err != nil {
		return model.Topic{}, fmt.Errorf("insert topic: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Topic{}, err
	}
	return model.Topic{
		ID: id, Subject: subject, Guidance: guidance, Status: model.StatusGenerating,
		TargetCount: targetCount, Stage: "queued", CreatedAt: now, UpdatedAt: now,
	}, nil
}

const topicCols = `id, subject, guidance, status, target_count, stage, progress, error, created_at, updated_at`

func scanTopic(sc interface{ Scan(...any) error }) (model.Topic, error) {
	var t model.Topic
	var status, created, updated string
	if err := sc.Scan(&t.ID, &t.Subject, &t.Guidance, &status, &t.TargetCount,
		&t.Stage, &t.Progress, &t.Error, &created, &updated); err != nil {
		return model.Topic{}, err
	}
	t.Status = model.TopicStatus(status)
	t.CreatedAt = mustParseTime(created)
	t.UpdatedAt = mustParseTime(updated)
	return t, nil
}

// GetTopic returns a single topic.
func (s *Store) GetTopic(id int64) (model.Topic, error) {
	t, err := scanTopic(s.db.QueryRow(`SELECT `+topicCols+` FROM topics WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Topic{}, ErrNotFound
	}
	if err != nil {
		return model.Topic{}, fmt.Errorf("get topic: %w", err)
	}
	return t, nil
}

// ListTopics returns every topic with its deck and mastery summary,
// most recently created first.
func (s *Store) ListTopics() ([]TopicSummary, error) {
	rows, err := s.db.Query(`SELECT ` + topicCols + ` FROM topics ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list topics: %w", err)
	}
	defer rows.Close()

	var topics []model.Topic
	for rows.Next() {
		t, err := scanTopic(rows)
		if err != nil {
			return nil, err
		}
		topics = append(topics, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]TopicSummary, 0, len(topics))
	for _, t := range topics {
		sum, err := s.summarize(t)
		if err != nil {
			return nil, err
		}
		out = append(out, sum)
	}
	return out, nil
}

// summarize computes deck counts and mastery for one topic.
func (s *Store) summarize(t model.Topic) (TopicSummary, error) {
	sum := TopicSummary{Topic: t}
	rows, err := s.db.Query(`
		SELECT r.stability, r.last_reviewed_at, r.due_at
		FROM facts f LEFT JOIN review_state r ON r.fact_id = f.id
		WHERE f.topic_id = ?`, t.ID)
	if err != nil {
		return sum, fmt.Errorf("summarize topic %d: %w", t.ID, err)
	}
	defer rows.Close()

	now := time.Now()
	var recallTotal, stabilityTotal float64
	for rows.Next() {
		var stability sql.NullFloat64
		var last, due sql.NullString
		if err := rows.Scan(&stability, &last, &due); err != nil {
			return sum, err
		}
		sum.FactCount++
		if !stability.Valid {
			// Never reviewed: contributes zero mastery and counts as new.
			sum.NewCount++
			continue
		}
		st := fsrs.State{Stability: stability.Float64, LastReview: mustParseTime(last.String), Reps: 1}
		recallTotal += st.Retrievability(now)
		stabilityTotal += stability.Float64
		if d := mustParseTime(due.String); !d.After(now) {
			sum.DueCount++
		}
	}
	if err := rows.Err(); err != nil {
		return sum, err
	}
	if sum.FactCount > 0 {
		// Mastery is mean predicted recall across the deck, so unlearned facts
		// drag it down — it answers "how much of this deck is in my head now?".
		sum.Mastery = recallTotal / float64(sum.FactCount)
	}
	if seen := sum.FactCount - sum.NewCount; seen > 0 {
		// Averaged over seen facts only: unseen facts have no stability to
		// average, and counting them as zero would flatten the curve to nothing.
		sum.Stability = stabilityTotal / float64(seen)
	}
	return sum, nil
}

// UpdateTopicProgress records generation progress for the polling UI.
func (s *Store) UpdateTopicProgress(id int64, stage string, progress int) error {
	_, err := s.db.Exec(`UPDATE topics SET stage = ?, progress = ?, updated_at = ? WHERE id = ?`,
		stage, progress, formatTime(time.Now()), id)
	if err != nil {
		return fmt.Errorf("update topic progress: %w", err)
	}
	return nil
}

// SetTopicStatus marks a topic ready or failed. errMsg is stored only for failures.
func (s *Store) SetTopicStatus(id int64, status model.TopicStatus, errMsg string) error {
	stage := "done"
	progress := 100
	if status == model.StatusFailed {
		stage = "failed"
	}
	_, err := s.db.Exec(
		`UPDATE topics SET status = ?, error = ?, stage = ?, progress = ?, updated_at = ? WHERE id = ?`,
		string(status), errMsg, stage, progress, formatTime(time.Now()), id)
	if err != nil {
		return fmt.Errorf("set topic status: %w", err)
	}
	return nil
}

// ResetForRegeneration empties a topic's deck and puts it back into the
// generating state, keeping its subject, guidance and target count.
//
// The deck's facts are deleted, which cascades to their questions, attempts and
// review state: the new deck is a different set of questions, so carrying the
// old scheduling forward would attach it to facts that no longer exist. The
// caller is responsible for warning the user when there is history to lose.
//
// Returns ErrBusy if generation is already running, so a double click cannot
// start two jobs against one topic.
func (s *Store) ResetForRegeneration(id int64) (model.Topic, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return model.Topic{}, err
	}
	defer tx.Rollback()

	var status string
	err = tx.QueryRow(`SELECT status FROM topics WHERE id = ?`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Topic{}, ErrNotFound
	}
	if err != nil {
		return model.Topic{}, fmt.Errorf("read topic status: %w", err)
	}
	if model.TopicStatus(status) == model.StatusGenerating {
		return model.Topic{}, ErrBusy
	}

	if _, err := tx.Exec(`DELETE FROM facts WHERE topic_id = ?`, id); err != nil {
		return model.Topic{}, fmt.Errorf("clear deck: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE topics SET status = ?, stage = 'queued', progress = 0, error = '', updated_at = ?
		 WHERE id = ?`,
		string(model.StatusGenerating), formatTime(time.Now()), id); err != nil {
		return model.Topic{}, fmt.Errorf("reset topic: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return model.Topic{}, err
	}
	return s.GetTopic(id)
}

// DeleteTopic removes a topic and, by cascade, its facts, questions,
// sessions, attempts and review state.
func (s *Store) DeleteTopic(id int64) error {
	res, err := s.db.Exec(`DELETE FROM topics WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete topic: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// InsertFact writes a fact and its question renderings in one transaction.
// A fact whose normalised statement already exists in the topic is skipped;
// inserted reports whether the fact was new.
func (s *Store) InsertFact(f model.Fact) (id int64, inserted bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()

	tags, err := json.Marshal(f.Tags)
	if err != nil {
		return 0, false, fmt.Errorf("encode tags: %w", err)
	}
	now := formatTime(time.Now())

	res, err := tx.Exec(`
		INSERT OR IGNORE INTO facts
			(topic_id, statement, canonical_answer, explanation, tags, difficulty, norm_key, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		f.TopicID, f.Statement, f.CanonicalAnswer, f.Explanation, string(tags),
		f.Difficulty, NormalizeKey(f.Statement), now)
	if err != nil {
		return 0, false, fmt.Errorf("insert fact: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, false, nil // duplicate; nothing written
	}
	id, err = res.LastInsertId()
	if err != nil {
		return 0, false, err
	}

	for _, q := range f.Questions {
		opts, err := json.Marshal(q.Options)
		if err != nil {
			return 0, false, fmt.Errorf("encode options: %w", err)
		}
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO questions (fact_id, mode, prompt, options, correct_option, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			id, string(q.Mode), q.Prompt, string(opts), q.CorrectOption, now); err != nil {
			return 0, false, fmt.Errorf("insert question: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// ListFacts returns a topic's facts with their questions and review state.
func (s *Store) ListFacts(topicID int64) ([]model.Fact, error) {
	rows, err := s.db.Query(`
		SELECT id, topic_id, statement, canonical_answer, explanation, tags, difficulty
		FROM facts WHERE topic_id = ? ORDER BY id`, topicID)
	if err != nil {
		return nil, fmt.Errorf("list facts: %w", err)
	}
	defer rows.Close()

	var facts []model.Fact
	byID := map[int64]int{}
	for rows.Next() {
		var f model.Fact
		var tags string
		if err := rows.Scan(&f.ID, &f.TopicID, &f.Statement, &f.CanonicalAnswer,
			&f.Explanation, &tags, &f.Difficulty); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(tags), &f.Tags); err != nil {
			f.Tags = nil
		}
		byID[f.ID] = len(facts)
		facts = append(facts, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(facts) == 0 {
		return facts, nil
	}

	qrows, err := s.db.Query(`
		SELECT q.id, q.fact_id, q.mode, q.prompt, q.options, q.correct_option
		FROM questions q JOIN facts f ON f.id = q.fact_id
		WHERE f.topic_id = ? ORDER BY q.fact_id, q.mode`, topicID)
	if err != nil {
		return nil, fmt.Errorf("list questions: %w", err)
	}
	defer qrows.Close()
	for qrows.Next() {
		q, err := scanQuestion(qrows)
		if err != nil {
			return nil, err
		}
		if i, ok := byID[q.FactID]; ok {
			facts[i].Questions = append(facts[i].Questions, q)
		}
	}
	if err := qrows.Err(); err != nil {
		return nil, err
	}

	rrows, err := s.db.Query(`
		SELECT r.fact_id, r.stability, r.difficulty, r.due_at, r.last_reviewed_at, r.reps, r.lapses
		FROM review_state r JOIN facts f ON f.id = r.fact_id
		WHERE f.topic_id = ?`, topicID)
	if err != nil {
		return nil, fmt.Errorf("list review state: %w", err)
	}
	defer rrows.Close()
	for rrows.Next() {
		r, err := scanReview(rrows)
		if err != nil {
			return nil, err
		}
		if i, ok := byID[r.FactID]; ok {
			rc := r
			facts[i].Review = &rc
		}
	}
	return facts, rrows.Err()
}

func scanQuestion(sc interface{ Scan(...any) error }) (model.Question, error) {
	var q model.Question
	var mode, options string
	if err := sc.Scan(&q.ID, &q.FactID, &mode, &q.Prompt, &options, &q.CorrectOption); err != nil {
		return q, err
	}
	q.Mode = model.Mode(mode)
	if err := json.Unmarshal([]byte(options), &q.Options); err != nil {
		q.Options = nil
	}
	return q, nil
}

func scanReview(sc interface{ Scan(...any) error }) (model.Review, error) {
	var r model.Review
	var due, last string
	if err := sc.Scan(&r.FactID, &r.Stability, &r.Difficulty, &due, &last, &r.Reps, &r.Lapses); err != nil {
		return r, err
	}
	r.DueAt = mustParseTime(due)
	r.LastReview = mustParseTime(last)
	return r, nil
}

// TopicSummaryByID returns one topic's summary.
func (s *Store) TopicSummaryByID(id int64) (TopicSummary, error) {
	t, err := s.GetTopic(id)
	if err != nil {
		return TopicSummary{}, err
	}
	return s.summarize(t)
}

// ResetStuckGenerations marks topics that were mid-generation when the process
// died as failed, so the UI never polls a job that no longer exists.
func (s *Store) ResetStuckGenerations() error {
	_, err := s.db.Exec(
		`UPDATE topics SET status = ?, error = ?, stage = 'failed', updated_at = ?
		 WHERE status = ?`,
		string(model.StatusFailed), "generation was interrupted by a restart",
		formatTime(time.Now()), string(model.StatusGenerating))
	return err
}

// NormalizeKey reduces a statement to a comparison key: lowercase, letters and
// digits only, single-spaced. Used for exact-duplicate rejection at insert time.
//
// Letters and digits are tested by Unicode category rather than ASCII range, so
// accented letters survive while non-ASCII punctuation — em dashes, guillemets,
// curly quotes — is stripped along with the ASCII kind.
func NormalizeKey(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := true // leading spaces are dropped
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}
