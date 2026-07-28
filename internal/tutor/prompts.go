package tutor

import (
	"fmt"
	"strings"

	"github.com/ahaley/trivial/internal/llm"
	"github.com/ahaley/trivial/internal/model"
)

const gradeSystem = `You grade a learner's free-text answer against a canonical answer.

Be lenient about spelling, capitalisation, word order, articles, and extra words:
the learner is typing from memory, not transcribing. Be strict about substance:
the specific entity, number, or claim must be right.

Choose exactly one verdict:
- "correct": the substance matches, however it is worded.
- "partial": part of the expected answer is there, or it is right but too vague to
  stand alone, or right for the wrong reason.
- "incorrect": the substance is wrong, or the answer is empty, or it says the
  learner does not know.

Then write one or two sentences addressed to the learner. For a correct answer,
confirm it and add one memorable detail. Otherwise, name precisely what was wrong
or missing and state the correct fact — that correction is the moment the learner
actually learns, so make it specific rather than merely encouraging.`

// gradeSchema constrains the grader's output.
var gradeSchema = &llm.Schema{
	Type: llm.TypeObject,
	Properties: map[string]*llm.Schema{
		"verdict":  {Type: llm.TypeString, Enum: []string{"correct", "partial", "incorrect"}},
		"critique": {Type: llm.TypeString},
	},
	PropertyOrdering: []string{"verdict", "critique"},
	Required:         []string{"verdict", "critique"},
}

func gradePrompt(subject, question, canonical, explanation, response string) string {
	return fmt.Sprintf(
		"Subject: %s\n\nQuestion: %s\nCanonical answer: %s\nWhy: %s\n\nThe learner answered:\n%s\n",
		subject, question, canonical, explanation, response)
}

const explainSystem = `You are a patient tutor. The learner has just answered a trivia
question and wants to understand the fact properly rather than only memorise it.

Write three or four short paragraphs of plain prose, with no headings or lists:
place the fact in its context, explain why it is so, connect it to something the
learner is likely to already know, and name the confusion this fact is most often
tangled with. If the learner answered wrongly, address that specific
misunderstanding directly. Do not restate the question.`

// explainSchema constrains the elaboration output.
var explainSchema = &llm.Schema{
	Type: llm.TypeObject,
	Properties: map[string]*llm.Schema{
		"elaboration": {Type: llm.TypeString},
	},
	Required: []string{"elaboration"},
}

func explainPrompt(subject string, f model.Fact, response string, grade model.Grade) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Subject: %s\n\nQuestion: %s\nCanonical answer: %s\nExplanation so far: %s\n",
		subject, f.Statement, f.CanonicalAnswer, f.Explanation)
	if response != "" {
		fmt.Fprintf(&b, "\nThe learner answered %q and was graded %s.\n", response, grade)
	}
	return b.String()
}
