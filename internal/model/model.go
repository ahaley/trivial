// Package model holds Trivial's domain types (SPEC.md §3), shared by the
// store, the generation pipeline, the pedagogy engine and the HTTP API.
package model

import "time"

// TopicStatus tracks a topic's generation lifecycle.
type TopicStatus string

const (
	StatusGenerating TopicStatus = "generating"
	StatusReady      TopicStatus = "ready"
	StatusFailed     TopicStatus = "failed"
)

// Topic is a user-created subject that owns a deck of facts.
type Topic struct {
	ID          int64       `json:"id"`
	Subject     string      `json:"subject"`
	Guidance    string      `json:"guidance,omitempty"`
	Status      TopicStatus `json:"status"`
	TargetCount int         `json:"target_count"`
	Stage       string      `json:"stage,omitempty"`
	Progress    int         `json:"progress"`
	Error       string      `json:"error,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

// Fact is the atom of knowledge that mastery is tracked against.
type Fact struct {
	ID              int64    `json:"id"`
	TopicID         int64    `json:"topic_id"`
	Statement       string   `json:"statement"`
	CanonicalAnswer string   `json:"canonical_answer"`
	Explanation     string   `json:"explanation"`
	Tags            []string `json:"tags"`
	Difficulty      int      `json:"difficulty"`

	// Questions are this fact's renderings; populated on detail reads.
	Questions []Question `json:"questions,omitempty"`
	// Review is the fact's scheduling state; populated on detail reads.
	Review *Review `json:"review,omitempty"`
}

// Mode is how a fact is rendered as a question.
type Mode string

const (
	// ModeOpen asks for a free-text answer — recall, the harder retrieval.
	ModeOpen Mode = "open"
	// ModeMC asks the user to pick from four options — recognition.
	ModeMC Mode = "mc"
)

// Valid reports whether m is a defined mode.
func (m Mode) Valid() bool { return m == ModeOpen || m == ModeMC }

// Question is one rendering of a fact.
type Question struct {
	ID      int64    `json:"id"`
	FactID  int64    `json:"fact_id"`
	Mode    Mode     `json:"mode"`
	Prompt  string   `json:"prompt"`
	Options []string `json:"options,omitempty"`
	// CorrectOption indexes Options for multiple choice; -1 for open-ended.
	CorrectOption int `json:"-"`
}

// SessionKind distinguishes how a session's queue was composed.
type SessionKind string

const (
	// KindTopic drills a single topic.
	KindTopic SessionKind = "topic"
	// KindReview interleaves everything due across all topics.
	KindReview SessionKind = "review"
	// KindDrill targets one weak subtopic tag.
	KindDrill SessionKind = "drill"
)

// Session is one quiz run.
type Session struct {
	ID         int64       `json:"id"`
	TopicID    *int64      `json:"topic_id,omitempty"`
	Kind       SessionKind `json:"kind"`
	Tag        string      `json:"tag,omitempty"`
	Planned    int         `json:"planned"`
	StartedAt  time.Time   `json:"started_at"`
	FinishedAt *time.Time  `json:"finished_at,omitempty"`
}

// Grade is the verdict on a single answer.
type Grade string

const (
	GradeCorrect   Grade = "correct"
	GradePartial   Grade = "partial"
	GradeIncorrect Grade = "incorrect"
)

// Valid reports whether g is a defined grade.
func (g Grade) Valid() bool {
	return g == GradeCorrect || g == GradePartial || g == GradeIncorrect
}

// Attempt is a recorded answer.
type Attempt struct {
	ID          int64     `json:"id"`
	SessionID   int64     `json:"session_id"`
	QuestionID  int64     `json:"question_id"`
	FactID      int64     `json:"fact_id"`
	Response    string    `json:"response"`
	Grade       Grade     `json:"grade"`
	Rating      int       `json:"rating"`
	LatencyMS   int       `json:"latency_ms"`
	Critique    string    `json:"critique,omitempty"`
	Elaboration string    `json:"elaboration,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// Review is a fact's FSRS scheduling state.
type Review struct {
	FactID     int64     `json:"fact_id"`
	Stability  float64   `json:"stability"`
	Difficulty float64   `json:"difficulty"`
	DueAt      time.Time `json:"due_at"`
	LastReview time.Time `json:"last_reviewed_at"`
	Reps       int       `json:"reps"`
	Lapses     int       `json:"lapses"`
}
