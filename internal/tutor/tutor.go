// Package tutor grades answers and elaborates on facts (SPEC.md §6).
//
// Multiple-choice answers are settled locally and instantly. Open-ended answers
// go to the model, which judges substance rather than spelling and writes the
// corrective feedback that follows a miss.
package tutor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ahaley/trivial/internal/fsrs"
	"github.com/ahaley/trivial/internal/llm"
	"github.com/ahaley/trivial/internal/model"
)

// Verdict is the outcome of grading one answer.
type Verdict struct {
	Grade model.Grade
	// Critique is feedback addressed to the learner: confirmation when correct,
	// a specific correction when not.
	Critique string
}

// Tutor grades answers and writes elaborations.
type Tutor struct {
	llm llm.Client
	log *slog.Logger
}

// New returns a Tutor.
func New(client llm.Client, log *slog.Logger) *Tutor {
	if log == nil {
		log = slog.Default()
	}
	return &Tutor{llm: client, log: log}
}

// GradeMC settles a multiple-choice answer locally. choice is the index the
// learner picked; anything out of range counts as incorrect.
func GradeMC(q model.Question, choice int) Verdict {
	if choice == q.CorrectOption && choice >= 0 && choice < len(q.Options) {
		return Verdict{Grade: model.GradeCorrect, Critique: "Correct."}
	}
	correct := ""
	if q.CorrectOption >= 0 && q.CorrectOption < len(q.Options) {
		correct = q.Options[q.CorrectOption]
	}
	if choice < 0 || choice >= len(q.Options) {
		return Verdict{Grade: model.GradeIncorrect,
			Critique: fmt.Sprintf("The correct option was %q.", correct)}
	}
	return Verdict{Grade: model.GradeIncorrect,
		Critique: fmt.Sprintf("Not quite — you chose %q; the correct option was %q.",
			q.Options[choice], correct)}
}

// GradeOpen grades a free-text answer against the fact's canonical answer.
// An empty response is incorrect without troubling the model. If the model call
// fails, grading degrades to the local word-overlap comparison rather than
// failing the whole answer submission.
func (t *Tutor) GradeOpen(ctx context.Context, subject string, f model.Fact, question, response string) Verdict {
	response = strings.TrimSpace(response)
	if response == "" {
		return Verdict{
			Grade:    model.GradeIncorrect,
			Critique: fmt.Sprintf("The answer is %s. %s", f.CanonicalAnswer, f.Explanation),
		}
	}

	var p struct {
		Verdict  string `json:"verdict"`
		Critique string `json:"critique"`
	}

	text, err := t.llm.Complete(ctx, llm.Request{
		Task:        llm.TaskGrade,
		System:      gradeSystem,
		Prompt:      gradePrompt(subject, question, f.CanonicalAnswer, f.Explanation, response),
		Schema:      gradeSchema,
		Temperature: 0.1, // grading should be as reproducible as the model allows
		// The verdict is two sentences, but judging an answer is exactly what a
		// reasoning model spends tokens on before writing anything. Too tight a
		// ceiling here silently demotes every answer to the offline fallback.
		MaxTokens: llm.ReasoningHeadroom,
		Vars: map[string]string{
			"answer":   f.CanonicalAnswer,
			"response": response,
		},
	})
	if err == nil {
		if derr := llm.DecodeJSON(text, &p); derr == nil {
			if g, ok := parseGrade(p.Verdict); ok {
				return Verdict{Grade: g, Critique: strings.TrimSpace(p.Critique)}
			}
		}
		err = fmt.Errorf("unusable grading response")
	}

	t.log.Warn("falling back to local grading", "fact", f.ID, "err", err)
	return GradeOpenLocal(f, response)
}

// GradeOpenLocal grades a free-text answer without the model: word-overlap
// similarity against the canonical answer. It is the grader when AI grading
// is switched off, and the fallback when the model is unreachable.
func GradeOpenLocal(f model.Fact, response string) Verdict {
	response = strings.TrimSpace(response)
	if response == "" {
		return Verdict{
			Grade:    model.GradeIncorrect,
			Critique: fmt.Sprintf("The answer is %s. %s", f.CanonicalAnswer, f.Explanation),
		}
	}
	switch sim := llm.Similarity(f.CanonicalAnswer, response); {
	case sim >= 0.8:
		return Verdict{Grade: model.GradeCorrect, Critique: "Correct."}
	case sim >= 0.35:
		return Verdict{
			Grade: model.GradePartial,
			Critique: fmt.Sprintf("Partly right — the expected answer is %s. %s",
				f.CanonicalAnswer, f.Explanation),
		}
	default:
		return Verdict{
			Grade:    model.GradeIncorrect,
			Critique: fmt.Sprintf("Not quite. The answer is %s. %s", f.CanonicalAnswer, f.Explanation),
		}
	}
}

// Explain writes the deeper elaboration behind the "explain more" button.
func (t *Tutor) Explain(ctx context.Context, subject string, f model.Fact, response string, grade model.Grade) (string, error) {
	var p struct {
		Elaboration string `json:"elaboration"`
	}
	text, err := t.llm.Complete(ctx, llm.Request{
		Task:        llm.TaskExplain,
		System:      explainSystem,
		Prompt:      explainPrompt(subject, f, response, grade),
		Schema:      explainSchema,
		Temperature: 0.6,
		// Several paragraphs of prose, plus reasoning headroom.
		MaxTokens: 8192,
		Vars: map[string]string{
			"statement": f.Statement,
			"answer":    f.CanonicalAnswer,
		},
	})
	if err != nil {
		return "", fmt.Errorf("explain: %w", err)
	}
	if err := llm.DecodeJSON(text, &p); err != nil {
		return "", fmt.Errorf("explain: unusable response: %w", err)
	}
	out := strings.TrimSpace(p.Elaboration)
	if out == "" {
		return "", fmt.Errorf("explain: model returned no elaboration")
	}
	return out, nil
}

// RatingFor maps a grade to the FSRS rating that drives scheduling (SPEC.md §6).
// A correct answer given quickly is treated as Easy: fluent recall is evidence
// of a stronger memory than a correct answer laboured over.
func RatingFor(g model.Grade, latencyMS int, mode model.Mode) fsrs.Rating {
	switch g {
	case model.GradeCorrect:
		if latencyMS > 0 && latencyMS < fastAnswerMS(mode) {
			return fsrs.Easy
		}
		return fsrs.Good
	case model.GradePartial:
		return fsrs.Hard
	default:
		return fsrs.Again
	}
}

// fastAnswerMS is the threshold below which an answer counts as fluent.
// Typing a free-text answer takes longer than clicking an option, so the
// open-ended threshold is more generous.
func fastAnswerMS(mode model.Mode) int {
	if mode == model.ModeOpen {
		return 12000
	}
	return 5000
}

func parseGrade(s string) (model.Grade, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "correct":
		return model.GradeCorrect, true
	case "partial", "partially_correct", "partially correct":
		return model.GradePartial, true
	case "incorrect", "wrong":
		return model.GradeIncorrect, true
	}
	return "", false
}
