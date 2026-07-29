package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ahaley/trivial/internal/fsrs"
	"github.com/ahaley/trivial/internal/model"
	"github.com/ahaley/trivial/internal/store"
	"github.com/ahaley/trivial/internal/tutor"
)

type createSessionRequest struct {
	// TopicID restricts the session to one topic. Omit it for a review session
	// that interleaves everything due across all topics.
	TopicID *int64 `json:"topic_id"`
	// Tag restricts the session to one weak subtopic — a targeted drill.
	Tag string `json:"tag"`
	// Size overrides the configured session length.
	Size int `json:"size"`
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req createSessionRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	req.Tag = strings.TrimSpace(req.Tag)

	size := req.Size
	if size <= 0 {
		size = s.cfg.SessionSize
	}
	if size > 200 {
		size = 200
	}

	if req.TopicID != nil {
		if _, err := s.store.GetTopic(*req.TopicID); writeStoreError(w, err, "topic") {
			return
		}
	}

	candidates, err := s.store.Candidates(req.TopicID, req.Tag)
	if writeStoreError(w, err, "candidates") {
		return
	}

	items := s.composer.Compose(candidates, size, time.Now())
	if len(items) == 0 {
		writeError(w, http.StatusConflict,
			"nothing to ask yet — generate a topic first, or come back when reviews fall due")
		return
	}

	kind := model.KindReview
	switch {
	case req.Tag != "":
		kind = model.KindDrill
	case req.TopicID != nil:
		kind = model.KindTopic
	}

	sess, err := s.store.CreateSession(req.TopicID, kind, req.Tag, items)
	if writeStoreError(w, err, "session") {
		return
	}
	writeJSON(w, http.StatusCreated, sess)
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid session id")
		return
	}
	summary, err := s.store.SessionSummary(id)
	if writeStoreError(w, err, "session") {
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// nextResponse is one step of the quiz loop: either the next question, or the
// end-of-session summary.
type nextResponse struct {
	Done     bool                 `json:"done"`
	Question *questionView        `json:"question,omitempty"`
	Progress progressView         `json:"progress"`
	Summary  *store.SessionResult `json:"summary,omitempty"`
}

// questionView is a question as the client may see it — deliberately without
// the correct answer, which is only revealed once an answer is submitted.
type questionView struct {
	ID       int64      `json:"id"`
	FactID   int64      `json:"fact_id"`
	Mode     model.Mode `json:"mode"`
	Prompt   string     `json:"prompt"`
	Options  []string   `json:"options,omitempty"`
	Subject  string     `json:"subject"`
	Tags     []string   `json:"tags"`
	IsRepeat bool       `json:"is_repeat"`
}

type progressView struct {
	Answered int `json:"answered"`
	Total    int `json:"total"`
}

func (s *Server) handleNext(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid session id")
		return
	}
	if _, err := s.store.GetSession(id); writeStoreError(w, err, "session") {
		return
	}

	answered, total, err := s.store.Progress(id)
	if writeStoreError(w, err, "session progress") {
		return
	}
	resp := nextResponse{Progress: progressView{Answered: answered, Total: total}}

	_, q, f, err := s.store.NextItem(id)
	if errors.Is(err, store.ErrQueueEmpty) {
		if err := s.store.FinishSession(id); err != nil {
			s.log.Warn("could not mark session finished", "session", id, "err", err)
		}
		summary, err := s.store.SessionSummary(id)
		if writeStoreError(w, err, "session summary") {
			return
		}
		resp.Done = true
		resp.Summary = &summary
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if writeStoreError(w, err, "next question") {
		return
	}

	subject := ""
	if t, err := s.store.GetTopic(f.TopicID); err == nil {
		subject = t.Subject
	}
	// A fact already attempted in this session is being re-asked for reinforcement.
	repeat := answered > 0 && s.alreadyAttempted(id, f.ID)

	resp.Question = &questionView{
		ID: q.ID, FactID: f.ID, Mode: q.Mode, Prompt: q.Prompt,
		Options: q.Options, Subject: subject, Tags: f.Tags, IsRepeat: repeat,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) alreadyAttempted(sessionID, factID int64) bool {
	var n int
	err := s.store.DB().QueryRow(
		`SELECT COUNT(*) FROM attempts WHERE session_id = ? AND fact_id = ?`, sessionID, factID).Scan(&n)
	return err == nil && n > 0
}

type answerRequest struct {
	QuestionID int64 `json:"question_id"`
	// Response is the free-text answer for open-ended questions.
	Response string `json:"response"`
	// Choice is the selected option index for multiple choice; -1 means none.
	Choice *int `json:"choice"`
	// LatencyMS is how long the answer took, used to distinguish fluent recall
	// from laboured recall when rating the review.
	LatencyMS int `json:"latency_ms"`
}

// answerResponse is the immediate feedback shown after every answer.
type answerResponse struct {
	AttemptID       int64       `json:"attempt_id"`
	Grade           model.Grade `json:"grade"`
	Critique        string      `json:"critique"`
	CanonicalAnswer string      `json:"canonical_answer"`
	Explanation     string      `json:"explanation"`
	// CorrectOption is the right index for multiple choice, else -1.
	CorrectOption int `json:"correct_option"`
	// NextDue is when the fact is scheduled to return.
	NextDue time.Time `json:"next_due"`
	// WillRepeat reports that the fact was re-queued in this session.
	WillRepeat bool         `json:"will_repeat"`
	Progress   progressView `json:"progress"`
}

func (s *Server) handleAnswer(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid session id")
		return
	}
	var req answerRequest
	if err := decodeBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if _, err := s.store.GetSession(id); writeStoreError(w, err, "session") {
		return
	}

	itemID, q, f, err := s.store.NextItem(id)
	if errors.Is(err, store.ErrQueueEmpty) {
		writeError(w, http.StatusConflict, "this session is already finished")
		return
	}
	if writeStoreError(w, err, "next question") {
		return
	}
	// Guard against a double submit or an answer to a stale question: the
	// client must be answering the question the queue is actually on.
	if req.QuestionID != 0 && req.QuestionID != q.ID {
		writeError(w, http.StatusConflict, "that question is no longer the current one")
		return
	}

	subject := ""
	if t, err := s.store.GetTopic(f.TopicID); err == nil {
		subject = t.Subject
	}

	// Multiple choice is settled locally and instantly; open-ended goes to the
	// model, which judges substance rather than spelling (SPEC.md §6).
	var verdict tutor.Verdict
	var recorded string
	switch q.Mode {
	case model.ModeMC:
		choice := -1
		if req.Choice != nil {
			choice = *req.Choice
		}
		verdict = tutor.GradeMC(q, choice)
		if choice >= 0 && choice < len(q.Options) {
			recorded = q.Options[choice]
		}
	default:
		recorded = strings.TrimSpace(req.Response)
		aiOn, err := s.store.AIEnabled()
		if err != nil {
			s.log.Warn("could not read settings; assuming AI grading on", "err", err)
			aiOn = true
		}
		if aiOn {
			verdict = s.tutor.GradeOpen(r.Context(), subject, f, q.Prompt, recorded)
		} else {
			verdict = tutor.GradeOpenLocal(f, recorded)
		}
	}

	now := time.Now()
	rating := tutor.RatingFor(verdict.Grade, req.LatencyMS, q.Mode)

	prior, err := s.store.GetReview(f.ID)
	if writeStoreError(w, err, "review state") {
		return
	}
	next := s.sched.Review(fsrs.State{
		Stability:  prior.Stability,
		Difficulty: prior.Difficulty,
		Due:        prior.DueAt,
		LastReview: prior.LastReview,
		Reps:       prior.Reps,
		Lapses:     prior.Lapses,
	}, rating, now)

	if err := s.store.SaveReview(model.Review{
		FactID: f.ID, Stability: next.Stability, Difficulty: next.Difficulty,
		DueAt: next.Due, LastReview: next.LastReview, Reps: next.Reps, Lapses: next.Lapses,
	}); writeStoreError(w, err, "review state") {
		return
	}

	attemptID, err := s.store.RecordAttempt(model.Attempt{
		SessionID: id, QuestionID: q.ID, FactID: f.ID, Response: recorded,
		Grade: verdict.Grade, Rating: int(rating), LatencyMS: req.LatencyMS,
		Critique: verdict.Critique,
	})
	if writeStoreError(w, err, "attempt") {
		return
	}
	if err := s.store.MarkAnswered(itemID); writeStoreError(w, err, "session item") {
		return
	}

	// Anything not fully correct comes back before the user leaves. The same
	// question is re-asked a few items later: by then other questions have
	// intervened, so it is a real retrieval attempt rather than an echo.
	willRepeat := false
	if verdict.Grade != model.GradeCorrect {
		queued, err := s.store.AppendRelearn(id, q.ID, f.ID)
		if err != nil {
			s.log.Warn("could not queue reinforcement", "session", id, "fact", f.ID, "err", err)
		}
		willRepeat = queued
	}

	answered, total, err := s.store.Progress(id)
	if writeStoreError(w, err, "session progress") {
		return
	}
	if answered >= total {
		if err := s.store.FinishSession(id); err != nil {
			s.log.Warn("could not mark session finished", "session", id, "err", err)
		}
	}

	writeJSON(w, http.StatusOK, answerResponse{
		AttemptID:       attemptID,
		Grade:           verdict.Grade,
		Critique:        verdict.Critique,
		CanonicalAnswer: f.CanonicalAnswer,
		Explanation:     f.Explanation,
		CorrectOption:   q.CorrectOption,
		NextDue:         next.Due,
		WillRepeat:      willRepeat,
		Progress:        progressView{Answered: answered, Total: total},
	})
}

func (s *Server) handleExplain(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid attempt id")
		return
	}
	ac, err := s.store.GetAttemptContext(id)
	if writeStoreError(w, err, "attempt") {
		return
	}
	// Elaborations are cached on the attempt: asking twice costs nothing.
	if ac.Attempt.Elaboration != "" {
		writeJSON(w, http.StatusOK, map[string]string{"elaboration": ac.Attempt.Elaboration})
		return
	}

	text, err := s.tutor.Explain(r.Context(), ac.Subject, ac.Fact, ac.Attempt.Response, ac.Attempt.Grade)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := s.store.SaveElaboration(id, text); err != nil {
		s.log.Warn("could not cache elaboration", "attempt", id, "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"elaboration": text})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.Stats(30)
	if writeStoreError(w, err, "stats") {
		return
	}
	writeJSON(w, http.StatusOK, stats)
}
