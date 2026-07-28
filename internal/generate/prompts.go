package generate

import (
	"fmt"
	"strings"

	"github.com/ahaley/trivial/internal/llm"
	"github.com/ahaley/trivial/internal/model"
)

// The quality bar from SPEC.md §5, stated once and shared by every prompt that
// produces question content.
const qualityBar = `Every question you write must meet this bar:
- Unambiguous: exactly one defensible answer, with no reading of the question that makes another answer correct.
- Self-contained: answerable without seeing any other question, and without pronouns referring to unstated context.
- Specific: no "which of the following" phrasing, no reference to options in the question text itself.
- Factual: verifiable, not opinion, and not dependent on the current date.`

const researchSystem = `You are a meticulous trivia author and subject-matter researcher.
You survey a subject and extract the facts a well-informed enthusiast should know,
then turn each into a crisp question with a single canonical answer.

` + qualityBar + `

Write the canonical answer as the shortest complete correct response — a name, date,
term or short phrase — not a sentence of prose. Write the explanation as one or two
sentences that justify the answer and add the context a learner needs to remember it.

Tag each fact with one to three short lowercase subtopic tags (for example
"naval battles", "chronology", "vocabulary"). Reuse tags across facts so that
related facts group together. Rate difficulty 1 (widely known) to 5 (specialist).`

// researchSchema constrains the research stage's output.
var researchSchema = &llm.Schema{
	Type: llm.TypeObject,
	Properties: map[string]*llm.Schema{
		"facts": {
			Type: llm.TypeArray,
			Items: &llm.Schema{
				Type: llm.TypeObject,
				Properties: map[string]*llm.Schema{
					"statement":   {Type: llm.TypeString, Description: "The question, as a single sentence."},
					"answer":      {Type: llm.TypeString, Description: "The shortest complete correct answer."},
					"explanation": {Type: llm.TypeString, Description: "One or two sentences justifying the answer."},
					"tags": {
						Type:  llm.TypeArray,
						Items: &llm.Schema{Type: llm.TypeString},
					},
					"difficulty": {Type: llm.TypeInteger, Minimum: llm.Num(1), Maximum: llm.Num(5)},
				},
				PropertyOrdering: []string{"statement", "answer", "explanation", "tags", "difficulty"},
				Required:         []string{"statement", "answer", "explanation", "tags", "difficulty"},
			},
		},
	},
	Required: []string{"facts"},
}

// researchPrompt asks for one batch of facts, steering away from what has
// already been covered so batches stay diverse.
func researchPrompt(subject, guidance string, count int, covered []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Subject: %s\n\n", subject)
	if guidance != "" {
		fmt.Fprintf(&b, "Additional direction from the user: %s\n\n", guidance)
	}
	fmt.Fprintf(&b, "Write %d trivia facts about this subject.\n", count)
	b.WriteString("Spread them across different aspects of the subject and across the full difficulty range; " +
		"do not cluster on one narrow corner.\n")

	if len(covered) > 0 {
		b.WriteString("\nThese questions already exist. Do not repeat them, and do not ask the " +
			"same fact from a different angle:\n")
		for _, c := range covered {
			fmt.Fprintf(&b, "- %s\n", c)
		}
	}
	return b.String()
}

const renderSystem = `You turn a trivia fact into the two ways it can be asked.

` + qualityBar + `

Produce:
1. An open-ended phrasing, answered from memory with no options shown.
2. A multiple-choice phrasing with exactly four options.

The multiple-choice distractors are the hard part. Each must be:
- Wrong, unambiguously.
- Plausible to someone who half-knows the subject — the same kind of thing as the
  answer (a person for a person, a year for a year, a term for a term), and drawn
  from the same subject area.
- Similar in length and specificity to the correct answer, so that its form gives
  nothing away.

Never use "all of the above", "none of the above", or joke options.`

// renderSchema constrains the render stage's output.
var renderSchema = &llm.Schema{
	Type: llm.TypeObject,
	Properties: map[string]*llm.Schema{
		"open_prompt": {Type: llm.TypeString, Description: "The open-ended phrasing."},
		"mc_prompt":   {Type: llm.TypeString, Description: "The multiple-choice phrasing."},
		"options": {
			Type:        llm.TypeArray,
			Description: "Exactly four options, one of which is correct.",
			Items:       &llm.Schema{Type: llm.TypeString},
		},
		"correct_option": {
			Type:        llm.TypeInteger,
			Description: "Zero-based index into options of the correct one.",
			Minimum:     llm.Num(0),
			Maximum:     llm.Num(3),
		},
	},
	PropertyOrdering: []string{"open_prompt", "mc_prompt", "options", "correct_option"},
	Required:         []string{"open_prompt", "mc_prompt", "options", "correct_option"},
}

func renderPrompt(subject string, f model.Fact) string {
	return fmt.Sprintf(
		"Subject: %s\n\nFact to render:\nQuestion: %s\nCanonical answer: %s\nExplanation: %s\n",
		subject, f.Statement, f.CanonicalAnswer, f.Explanation)
}
