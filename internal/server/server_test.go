package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/ahaley/trivial/internal/config"
	"github.com/ahaley/trivial/internal/llm"
	"github.com/ahaley/trivial/internal/model"
	"github.com/ahaley/trivial/internal/store"
	"github.com/ahaley/trivial/web"
)

type harness struct {
	t   *testing.T
	srv *Server
	mux http.Handler
	st  *store.Store
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "srv.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	cfg := config.Config{
		Provider: "mock", Model: "mock", TargetCount: 8, SessionSize: 6,
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	srv := New(cfg, st, llm.NewMock(), quiet)
	t.Cleanup(func() {
		srv.Shutdown()
		st.Close()
	})
	return &harness{t: t, srv: srv, mux: srv.Handler(), st: st}
}

// do issues a request and decodes the response into out, asserting the status.
func (h *harness) do(method, path string, body, out any, wantStatus int) *httptest.ResponseRecorder {
	h.t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("encode body: %v", err)
		}
		rdr = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)

	if rec.Code != wantStatus {
		h.t.Fatalf("%s %s: status %d, want %d (body %s)", method, path, rec.Code, wantStatus, rec.Body.String())
	}
	if out != nil && rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			h.t.Fatalf("%s %s: decode response: %v (body %s)", method, path, err, rec.Body.String())
		}
	}
	return rec
}

// readyTopic creates a topic and waits for background generation to finish.
func (h *harness) readyTopic(subject string, count int) store.TopicSummary {
	h.t.Helper()
	var topic model.Topic
	h.do(http.MethodPost, "/api/v1/topics",
		map[string]any{"subject": subject, "target_count": count}, &topic, http.StatusAccepted)
	return h.awaitReady(topic.ID)
}

// awaitReady polls until generation finishes, then returns the topic summary.
func (h *harness) awaitReady(id int64) store.TopicSummary {
	h.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var gen generationStatus
		h.do(http.MethodGet, "/api/v1/topics/"+itoa(id)+"/generation", nil, &gen, http.StatusOK)
		switch gen.Status {
		case model.StatusReady:
			var summary store.TopicSummary
			h.do(http.MethodGet, "/api/v1/topics/"+itoa(id), nil, &summary, http.StatusOK)
			return summary
		case model.StatusFailed:
			h.t.Fatalf("generation failed: %s", gen.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
	h.t.Fatal("generation did not finish in time")
	return store.TopicSummary{}
}

// factIDs is the set of fact row IDs currently in a topic.
func (h *harness) factIDs(topicID int64) map[int64]bool {
	h.t.Helper()
	rows, err := h.st.DB().Query(`SELECT id FROM facts WHERE topic_id = ?`, topicID)
	if err != nil {
		h.t.Fatalf("read fact ids: %v", err)
	}
	defer rows.Close()

	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			h.t.Fatalf("scan fact id: %v", err)
		}
		out[id] = true
	}
	if err := rows.Err(); err != nil {
		h.t.Fatalf("read fact ids: %v", err)
	}
	return out
}

func itoa(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestHealth(t *testing.T) {
	h := newHarness(t)
	var got map[string]any
	h.do(http.MethodGet, "/api/v1/health", nil, &got, http.StatusOK)
	if got["ok"] != true || got["provider"] != "mock" {
		t.Errorf("health = %v", got)
	}
}

func TestCreateTopicValidation(t *testing.T) {
	h := newHarness(t)

	h.do(http.MethodPost, "/api/v1/topics", map[string]any{"subject": "  "}, nil, http.StatusBadRequest)
	h.do(http.MethodPost, "/api/v1/topics", map[string]any{"subject": longString(201)}, nil, http.StatusBadRequest)
	// Unknown fields are rejected so a client typo cannot silently do nothing.
	h.do(http.MethodPost, "/api/v1/topics",
		map[string]any{"subject": "Rome", "typo": 1}, nil, http.StatusBadRequest)

	// An oversized deck request is clamped rather than refused.
	var topic model.Topic
	h.do(http.MethodPost, "/api/v1/topics",
		map[string]any{"subject": "Rome", "target_count": 5000}, &topic, http.StatusAccepted)
	if topic.TargetCount != 100 {
		t.Errorf("target count = %d, want it clamped to 100", topic.TargetCount)
	}
}

func longString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

func TestTopicNotFound(t *testing.T) {
	h := newHarness(t)
	h.do(http.MethodGet, "/api/v1/topics/9999", nil, nil, http.StatusNotFound)
	h.do(http.MethodGet, "/api/v1/topics/notanumber", nil, nil, http.StatusBadRequest)
	h.do(http.MethodDelete, "/api/v1/topics/9999", nil, nil, http.StatusNotFound)
	h.do(http.MethodGet, "/api/v1/sessions/9999/next", nil, nil, http.StatusNotFound)
	h.do(http.MethodPost, "/api/v1/attempts/9999/explain", nil, nil, http.StatusNotFound)
	h.do(http.MethodGet, "/api/v1/nope", nil, nil, http.StatusNotFound)
}

// The whole loop: generate a deck, start a session, answer every question,
// reach the summary.
func TestQuizLoop(t *testing.T) {
	h := newHarness(t)
	topic := h.readyTopic("The Roman Republic", 8)
	if topic.FactCount == 0 {
		t.Fatal("generation produced no facts")
	}

	var sess model.Session
	h.do(http.MethodPost, "/api/v1/sessions",
		map[string]any{"topic_id": topic.ID, "size": 5}, &sess, http.StatusCreated)
	if sess.Kind != model.KindTopic || sess.Planned == 0 {
		t.Fatalf("unexpected session: %+v", sess)
	}

	answered, misses := 0, 0
	for {
		var next nextResponse
		h.do(http.MethodGet, "/api/v1/sessions/"+itoa(sess.ID)+"/next", nil, &next, http.StatusOK)
		if next.Done {
			if next.Summary == nil {
				t.Fatal("a finished session must carry its summary")
			}
			if next.Summary.Answered != answered {
				t.Errorf("summary counts %d answers, we sent %d", next.Summary.Answered, answered)
			}
			break
		}
		if next.Question == nil {
			t.Fatal("an unfinished session must offer a question")
		}
		// The client must never be told the answer before answering.
		raw, _ := json.Marshal(next.Question)
		if bytes.Contains(raw, []byte("canonical_answer")) || bytes.Contains(raw, []byte("correct_option")) {
			t.Fatalf("the question leaked its answer: %s", raw)
		}

		// Alternate right and wrong to exercise both paths and relearning.
		body := map[string]any{"question_id": next.Question.ID, "latency_ms": 4000}
		wantRight := answered%2 == 0
		if wantRight {
			body["response"] = h.canonicalFor(next.Question.FactID)
		} else {
			body["response"] = "definitely not the answer"
		}

		var ans answerResponse
		h.do(http.MethodPost, "/api/v1/sessions/"+itoa(sess.ID)+"/answer", body, &ans, http.StatusOK)
		if wantRight && ans.Grade != model.GradeCorrect {
			t.Errorf("answer %d: grade %q, want correct", answered, ans.Grade)
		}
		if !wantRight {
			if ans.Grade == model.GradeCorrect {
				t.Errorf("answer %d: a wrong answer was graded correct", answered)
			}
			misses++
		}
		if ans.CanonicalAnswer == "" {
			t.Errorf("answer %d: feedback should reveal the answer", answered)
		}
		if ans.NextDue.Before(time.Now()) {
			t.Errorf("answer %d: next due %v is in the past", answered, ans.NextDue)
		}

		answered++
		if answered > 40 {
			t.Fatal("the session did not terminate")
		}
	}

	if misses == 0 {
		t.Fatal("the test never missed a question, so relearning went untested")
	}
	// Misses are re-asked, so more answers than the plan, but bounded.
	if answered <= sess.Planned || answered > sess.Planned*2 {
		t.Errorf("answered %d for a %d-question plan; want more (repeats) but at most double",
			answered, sess.Planned)
	}

	// Answering a finished session is a conflict, not a crash.
	h.do(http.MethodPost, "/api/v1/sessions/"+itoa(sess.ID)+"/answer",
		map[string]any{"question_id": 1, "response": "x", "latency_ms": 1}, nil, http.StatusConflict)
}

// canonicalFor reads a fact's answer straight from the store — the API
// deliberately will not tell the client.
func (h *harness) canonicalFor(factID int64) string {
	h.t.Helper()
	var answer string
	if err := h.st.DB().QueryRow(`SELECT canonical_answer FROM facts WHERE id = ?`, factID).Scan(&answer); err != nil {
		h.t.Fatalf("look up fact %d: %v", factID, err)
	}
	return answer
}

// Submitting an answer to a question that is not the current one must be
// refused, so a double submit cannot consume two queue items.
func TestAnswerRejectsStaleQuestion(t *testing.T) {
	h := newHarness(t)
	topic := h.readyTopic("Rome", 6)

	var sess model.Session
	h.do(http.MethodPost, "/api/v1/sessions",
		map[string]any{"topic_id": topic.ID, "size": 3}, &sess, http.StatusCreated)

	var next nextResponse
	h.do(http.MethodGet, "/api/v1/sessions/"+itoa(sess.ID)+"/next", nil, &next, http.StatusOK)

	h.do(http.MethodPost, "/api/v1/sessions/"+itoa(sess.ID)+"/answer",
		map[string]any{"question_id": next.Question.ID + 9999, "response": "x", "latency_ms": 1},
		nil, http.StatusConflict)
}

func TestSessionWithNothingToAskIsRefused(t *testing.T) {
	h := newHarness(t)
	// No topics at all.
	h.do(http.MethodPost, "/api/v1/sessions", map[string]any{}, nil, http.StatusConflict)

	topic := h.readyTopic("Rome", 5)
	// A tag nothing carries.
	h.do(http.MethodPost, "/api/v1/sessions",
		map[string]any{"topic_id": topic.ID, "tag": "no such tag"}, nil, http.StatusConflict)
}

func TestExplainIsCached(t *testing.T) {
	h := newHarness(t)
	topic := h.readyTopic("Rome", 5)

	var sess model.Session
	h.do(http.MethodPost, "/api/v1/sessions",
		map[string]any{"topic_id": topic.ID, "size": 1}, &sess, http.StatusCreated)
	var next nextResponse
	h.do(http.MethodGet, "/api/v1/sessions/"+itoa(sess.ID)+"/next", nil, &next, http.StatusOK)

	var ans answerResponse
	h.do(http.MethodPost, "/api/v1/sessions/"+itoa(sess.ID)+"/answer",
		map[string]any{"question_id": next.Question.ID, "response": "wrong", "latency_ms": 100},
		&ans, http.StatusOK)

	var first, second map[string]string
	h.do(http.MethodPost, "/api/v1/attempts/"+itoa(ans.AttemptID)+"/explain", nil, &first, http.StatusOK)
	if first["elaboration"] == "" {
		t.Fatal("no elaboration returned")
	}
	h.do(http.MethodPost, "/api/v1/attempts/"+itoa(ans.AttemptID)+"/explain", nil, &second, http.StatusOK)
	if second["elaboration"] != first["elaboration"] {
		t.Error("a second explain should return the cached text")
	}

	var stored string
	h.st.DB().QueryRow(`SELECT elaboration FROM attempts WHERE id = ?`, ans.AttemptID).Scan(&stored)
	if stored != first["elaboration"] {
		t.Error("the elaboration should be persisted on the attempt")
	}
}

// Regenerating rebuilds the deck from the same subject, which is what makes a
// generation that failed for a passing reason recoverable.
func TestRegenerateTopic(t *testing.T) {
	h := newHarness(t)
	topic := h.readyTopic("The Roman Republic", 6)
	if topic.FactCount == 0 {
		t.Fatal("nothing generated to begin with")
	}
	firstFacts := h.factIDs(topic.ID)

	var restarted store.TopicSummary
	h.do(http.MethodPost, "/api/v1/topics/"+itoa(topic.ID)+"/regenerate", nil,
		&restarted, http.StatusAccepted)
	if restarted.Status != model.StatusGenerating {
		t.Errorf("status = %q, want generating", restarted.Status)
	}
	// The subject and settings must survive, or this is just a slower delete.
	if restarted.Subject != topic.Subject || restarted.TargetCount != topic.TargetCount {
		t.Errorf("settings not preserved: %+v", restarted)
	}

	final := h.awaitReady(topic.ID)
	if final.FactCount == 0 {
		t.Fatal("regeneration produced no facts")
	}
	// A fresh deck, not the old rows left in place.
	for id := range h.factIDs(topic.ID) {
		if firstFacts[id] {
			t.Errorf("fact %d survived regeneration; the old deck should be gone", id)
		}
	}
}

// A topic mid-generation must not be restarted underneath itself.
func TestRegenerateRejectsBusyTopic(t *testing.T) {
	h := newHarness(t)
	var topic model.Topic
	h.do(http.MethodPost, "/api/v1/topics",
		map[string]any{"subject": "Rome", "target_count": 8}, &topic, http.StatusAccepted)

	// The topic is generating from the moment it is created.
	h.do(http.MethodPost, "/api/v1/topics/"+itoa(topic.ID)+"/regenerate", nil, nil,
		http.StatusConflict)

	h.awaitReady(topic.ID) // let the job finish before the harness tears down
}

func TestRegenerateFailedTopic(t *testing.T) {
	h := newHarness(t)
	topic := h.readyTopic("Rome", 5)

	// Put it in the state a real failure leaves behind.
	if err := h.st.SetTopicStatus(topic.ID, model.StatusFailed, "model not available"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	h.do(http.MethodPost, "/api/v1/topics/"+itoa(topic.ID)+"/regenerate", nil, nil,
		http.StatusAccepted)

	final := h.awaitReady(topic.ID)
	if final.FactCount == 0 {
		t.Error("a retried topic should end up with a deck")
	}
	if final.Error != "" {
		t.Errorf("the old failure should be cleared, got %q", final.Error)
	}
}

func TestRegenerateUnknownTopic(t *testing.T) {
	h := newHarness(t)
	h.do(http.MethodPost, "/api/v1/topics/9999/regenerate", nil, nil, http.StatusNotFound)
}

func TestDeleteTopic(t *testing.T) {
	h := newHarness(t)
	topic := h.readyTopic("Rome", 5)

	h.do(http.MethodDelete, "/api/v1/topics/"+itoa(topic.ID), nil, nil, http.StatusNoContent)
	h.do(http.MethodGet, "/api/v1/topics/"+itoa(topic.ID), nil, nil, http.StatusNotFound)

	var topics []store.TopicSummary
	h.do(http.MethodGet, "/api/v1/topics", nil, &topics, http.StatusOK)
	if len(topics) != 0 {
		t.Errorf("got %d topics after deleting the only one", len(topics))
	}
}

func TestStatsEndpoint(t *testing.T) {
	h := newHarness(t)
	var stats store.Stats
	h.do(http.MethodGet, "/api/v1/stats", nil, &stats, http.StatusOK)
	if stats.Totals.Facts != 0 {
		t.Errorf("a fresh install should have no facts, got %d", stats.Totals.Facts)
	}

	h.readyTopic("Rome", 5)
	h.do(http.MethodGet, "/api/v1/stats", nil, &stats, http.StatusOK)
	if stats.Totals.Topics != 1 || stats.Totals.Facts == 0 {
		t.Errorf("totals = %+v", stats.Totals)
	}
	if len(stats.DueQueue) == 0 || len(stats.History) == 0 {
		t.Error("stats should always include the queue and history series")
	}
}

// Unknown paths fall through to the SPA so client-side routes survive a reload.
//
// The frontend is a generated artifact, so a checkout that has not run
// `npm run build` has none embedded. Both states are legitimate; what matters
// is that neither serves a bare 404.
func TestSPAFallback(t *testing.T) {
	h := newHarness(t)
	built := web.Built()

	for _, path := range []string{"/", "/progress", "/topics/12", "/sessions/3"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.mux.ServeHTTP(rec, req)

		if built {
			if rec.Code != http.StatusOK {
				t.Errorf("%s: status %d, want 200", path, rec.Code)
			}
			if !bytes.Contains(rec.Body.Bytes(), []byte(`<div id="root">`)) {
				t.Errorf("%s: did not serve the app shell", path)
			}
			continue
		}

		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status %d, want 503 when no frontend is built", path, rec.Code)
		}
		// The page has to say how to fix it, or it is just a prettier 404.
		if !bytes.Contains(rec.Body.Bytes(), []byte("npm run build")) {
			t.Errorf("%s: the unbuilt page should say how to build the frontend", path)
		}
	}

	// The API must work either way — it is the half that does not need Node.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("health: status %d, want 200 regardless of the frontend", rec.Code)
	}
}
