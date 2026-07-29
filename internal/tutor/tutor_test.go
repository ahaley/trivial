package tutor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ahaley/trivial/internal/fsrs"
	"github.com/ahaley/trivial/internal/llm"
	"github.com/ahaley/trivial/internal/model"
)

var fact = model.Fact{
	ID:              1,
	Statement:       "Which battle gave Rome its first naval victory?",
	CanonicalAnswer: "Mylae",
	Explanation:     "Fought in 260 BC during the First Punic War.",
}

func mcQuestion() model.Question {
	return model.Question{
		Mode:          model.ModeMC,
		Prompt:        fact.Statement,
		Options:       []string{"Cannae", "Mylae", "Zama", "Actium"},
		CorrectOption: 1,
	}
}

func TestGradeMC(t *testing.T) {
	q := mcQuestion()

	if v := GradeMC(q, 1); v.Grade != model.GradeCorrect {
		t.Errorf("picking the right option should be correct, got %q", v.Grade)
	}
	v := GradeMC(q, 0)
	if v.Grade != model.GradeIncorrect {
		t.Errorf("picking a wrong option should be incorrect, got %q", v.Grade)
	}
	// The correction must name both what was chosen and what was right.
	if !strings.Contains(v.Critique, "Cannae") || !strings.Contains(v.Critique, "Mylae") {
		t.Errorf("critique should name both answers, got %q", v.Critique)
	}
	// Out-of-range choices are a non-answer, not a crash.
	for _, choice := range []int{-1, 4, 99} {
		v := GradeMC(q, choice)
		if v.Grade != model.GradeIncorrect {
			t.Errorf("choice %d should be incorrect, got %q", choice, v.Grade)
		}
		if !strings.Contains(v.Critique, "Mylae") {
			t.Errorf("choice %d: critique should still reveal the answer, got %q", choice, v.Critique)
		}
	}
}

// stubClient returns a canned response, or an error.
type stubClient struct {
	reply string
	err   error
	last  llm.Request
}

func (s *stubClient) Name() string { return "stub" }
func (s *stubClient) Complete(_ context.Context, req llm.Request) (string, error) {
	s.last = req
	return s.reply, s.err
}

func TestGradeOpenUsesModelVerdict(t *testing.T) {
	stub := &stubClient{reply: `{"verdict":"partial","critique":"Right war, wrong battle."}`}
	tu := New(stub, quietLogger())

	v := tu.GradeOpen(context.Background(), "Rome", fact, fact.Statement, "some naval battle")
	if v.Grade != model.GradePartial {
		t.Errorf("grade = %q, want partial", v.Grade)
	}
	if v.Critique != "Right war, wrong battle." {
		t.Errorf("critique = %q", v.Critique)
	}
	// The grader must be given both sides of the comparison.
	if stub.last.Var("answer") != "Mylae" || stub.last.Var("response") != "some naval battle" {
		t.Errorf("grader request missing its inputs: %+v", stub.last.Vars)
	}
}

func TestGradeOpenRejectsEmptyAnswerWithoutCallingModel(t *testing.T) {
	stub := &stubClient{reply: `{"verdict":"correct","critique":"nope"}`}
	tu := New(stub, quietLogger())

	v := tu.GradeOpen(context.Background(), "Rome", fact, fact.Statement, "   ")
	if v.Grade != model.GradeIncorrect {
		t.Errorf("an empty answer must be incorrect, got %q", v.Grade)
	}
	if stub.last.Task != "" {
		t.Error("an empty answer should not reach the model at all")
	}
	if !strings.Contains(v.Critique, "Mylae") {
		t.Errorf("critique should reveal the answer, got %q", v.Critique)
	}
}

// A failing model must not fail the whole answer submission.
func TestGradeOpenFallsBackWhenModelFails(t *testing.T) {
	tu := New(&stubClient{err: errors.New("network down")}, quietLogger())

	if v := tu.GradeOpen(context.Background(), "Rome", fact, fact.Statement, "Mylae"); v.Grade != model.GradeCorrect {
		t.Errorf("an exact match should survive the fallback, got %q", v.Grade)
	}
	// Case and punctuation must not matter even offline.
	if v := tu.GradeOpen(context.Background(), "Rome", fact, fact.Statement, "  mylae! "); v.Grade != model.GradeCorrect {
		t.Errorf("fallback should normalise, got %q", v.Grade)
	}
	v := tu.GradeOpen(context.Background(), "Rome", fact, fact.Statement, "Actium")
	if v.Grade != model.GradeIncorrect {
		t.Errorf("a wrong answer should be incorrect offline, got %q", v.Grade)
	}
	if !strings.Contains(v.Critique, "Mylae") {
		t.Errorf("critique should reveal the answer, got %q", v.Critique)
	}
}

func TestGradeOpenLocal(t *testing.T) {
	cases := []struct {
		name     string
		response string
		want     model.Grade
	}{
		{"exact match", "Mylae", model.GradeCorrect},
		{"containment", "the Battle of Mylae", model.GradeCorrect},
		{"unrelated", "Actium", model.GradeIncorrect},
		{"empty", "   ", model.GradeIncorrect},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := GradeOpenLocal(fact, c.response)
			if v.Grade != c.want {
				t.Errorf("GradeOpenLocal(%q) = %q, want %q", c.response, v.Grade, c.want)
			}
			if v.Grade != model.GradeCorrect && !strings.Contains(v.Critique, "Mylae") {
				t.Errorf("critique should reveal the answer, got %q", v.Critique)
			}
		})
	}

	// Word overlap without containment: a multi-word answer answered half-right.
	partial := model.Fact{
		CanonicalAnswer: "Gaius Duilius commanded the fleet",
		Explanation:     "He won at Mylae in 260 BC.",
	}
	v := GradeOpenLocal(partial, "Duilius commanded the army")
	if v.Grade != model.GradePartial {
		t.Errorf("half-overlapping answer should be partial, got %q", v.Grade)
	}
	if !strings.Contains(v.Critique, partial.CanonicalAnswer) {
		t.Errorf("partial critique should name the expected answer, got %q", v.Critique)
	}
}

func TestGradeOpenFallsBackOnUnusableResponse(t *testing.T) {
	tu := New(&stubClient{reply: "not json at all"}, quietLogger())
	if v := tu.GradeOpen(context.Background(), "Rome", fact, fact.Statement, "Mylae"); v.Grade != model.GradeCorrect {
		t.Errorf("should fall back to local grading, got %q", v.Grade)
	}

	tu = New(&stubClient{reply: `{"verdict":"maybe","critique":"?"}`}, quietLogger())
	if v := tu.GradeOpen(context.Background(), "Rome", fact, fact.Statement, "Actium"); v.Grade != model.GradeIncorrect {
		t.Errorf("an unknown verdict should fall back, got %q", v.Grade)
	}
}

func TestExplain(t *testing.T) {
	tu := New(&stubClient{reply: `{"elaboration":"The First Punic War began in 264 BC."}`}, quietLogger())
	got, err := tu.Explain(context.Background(), "Rome", fact, "Actium", model.GradeIncorrect)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if got != "The First Punic War began in 264 BC." {
		t.Errorf("elaboration = %q", got)
	}

	tu = New(&stubClient{reply: `{"elaboration":"  "}`}, quietLogger())
	if _, err := tu.Explain(context.Background(), "Rome", fact, "", model.GradeCorrect); err == nil {
		t.Error("an empty elaboration should be an error, not empty output")
	}

	tu = New(&stubClient{err: errors.New("boom")}, quietLogger())
	if _, err := tu.Explain(context.Background(), "Rome", fact, "", model.GradeCorrect); err == nil {
		t.Error("a failed call should surface as an error")
	}
}

func TestRatingFor(t *testing.T) {
	// Fluent recall is stronger evidence than laboured recall.
	if got := RatingFor(model.GradeCorrect, 1000, model.ModeMC); got != fsrs.Easy {
		t.Errorf("a fast correct answer should rate Easy, got %v", got)
	}
	if got := RatingFor(model.GradeCorrect, 30000, model.ModeMC); got != fsrs.Good {
		t.Errorf("a slow correct answer should rate Good, got %v", got)
	}
	// Typing takes longer than clicking, so the open-ended bar is looser.
	if got := RatingFor(model.GradeCorrect, 8000, model.ModeOpen); got != fsrs.Easy {
		t.Errorf("8s typing a correct answer should still rate Easy, got %v", got)
	}
	if got := RatingFor(model.GradeCorrect, 8000, model.ModeMC); got != fsrs.Good {
		t.Errorf("8s clicking an option should rate Good, got %v", got)
	}
	// An unmeasured latency must not be mistaken for instant recall.
	if got := RatingFor(model.GradeCorrect, 0, model.ModeOpen); got != fsrs.Good {
		t.Errorf("a missing latency should rate Good, got %v", got)
	}
	if got := RatingFor(model.GradePartial, 1000, model.ModeOpen); got != fsrs.Hard {
		t.Errorf("partial should rate Hard, got %v", got)
	}
	if got := RatingFor(model.GradeIncorrect, 1000, model.ModeOpen); got != fsrs.Again {
		t.Errorf("incorrect should rate Again, got %v", got)
	}
}
