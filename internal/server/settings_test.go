package server

import (
	"net/http"
	"testing"

	"github.com/ahaley/trivial/internal/model"
)

func TestSettingsDefaultAndRoundTrip(t *testing.T) {
	h := newHarness(t)

	var s settingsView
	h.do(http.MethodGet, "/api/v1/settings", nil, &s, http.StatusOK)
	if !s.AIEnabled {
		t.Fatal("settings should default to AI grading on")
	}

	h.do(http.MethodPut, "/api/v1/settings", map[string]any{"ai_enabled": false}, &s, http.StatusOK)
	if s.AIEnabled {
		t.Fatal("PUT should echo the new value")
	}

	h.do(http.MethodGet, "/api/v1/settings", nil, &s, http.StatusOK)
	if s.AIEnabled {
		t.Fatal("the setting should persist")
	}
}

func TestSettingsRejectsBadBodies(t *testing.T) {
	h := newHarness(t)

	// An empty object must not silently switch AI grading off.
	h.do(http.MethodPut, "/api/v1/settings", map[string]any{}, nil, http.StatusBadRequest)
	// Typoed keys are rejected rather than ignored.
	h.do(http.MethodPut, "/api/v1/settings", map[string]any{"ai_enable": false}, nil, http.StatusBadRequest)
}

// With AI off, open answers are graded by the local word-overlap path, not the
// provider. The critiques differ ("Correct." vs the mock's "Matches the
// expected answer."), which is how this test tells the two paths apart.
func TestAnswerGradesLocallyWhenAIOff(t *testing.T) {
	h := newHarness(t)
	topic := h.readyTopic("The Roman Republic", 4)

	h.do(http.MethodPut, "/api/v1/settings", map[string]any{"ai_enabled": false}, nil, http.StatusOK)

	var sess model.Session
	h.do(http.MethodPost, "/api/v1/sessions",
		map[string]any{"topic_id": topic.ID, "size": 4}, &sess, http.StatusCreated)

	var next nextResponse
	h.do(http.MethodGet, "/api/v1/sessions/"+itoa(sess.ID)+"/next", nil, &next, http.StatusOK)
	if next.Question == nil || next.Question.Mode != model.ModeOpen {
		t.Fatalf("expected an open question first, got %+v", next.Question)
	}

	var ans answerResponse
	h.do(http.MethodPost, "/api/v1/sessions/"+itoa(sess.ID)+"/answer",
		map[string]any{
			"question_id": next.Question.ID,
			"response":    h.canonicalFor(next.Question.FactID),
			"latency_ms":  2000,
		}, &ans, http.StatusOK)
	if ans.Grade != model.GradeCorrect {
		t.Fatalf("canonical answer should grade correct locally, got %q (%s)", ans.Grade, ans.Critique)
	}
	if ans.Critique != "Correct." {
		t.Errorf("critique %q suggests the provider graded this, want the local \"Correct.\"", ans.Critique)
	}

	// Switch back on: the mock provider's wording returns.
	h.do(http.MethodPut, "/api/v1/settings", map[string]any{"ai_enabled": true}, nil, http.StatusOK)
	h.do(http.MethodGet, "/api/v1/sessions/"+itoa(sess.ID)+"/next", nil, &next, http.StatusOK)
	if next.Question == nil || next.Question.Mode != model.ModeOpen {
		t.Fatalf("expected another open question, got %+v", next.Question)
	}
	h.do(http.MethodPost, "/api/v1/sessions/"+itoa(sess.ID)+"/answer",
		map[string]any{
			"question_id": next.Question.ID,
			"response":    h.canonicalFor(next.Question.FactID),
			"latency_ms":  2000,
		}, &ans, http.StatusOK)
	if ans.Critique != "Matches the expected answer." {
		t.Errorf("critique %q, want the mock provider's wording once AI is back on", ans.Critique)
	}
}
