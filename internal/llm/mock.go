package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
)

// Mock is an offline provider. It produces schema-shaped, deterministic
// responses so the whole application — generation, quizzing, grading,
// scheduling — can be run and tested without an API key or network access.
//
// Its content is synthetic and clearly labelled as such; it is a harness, not
// a trivia source.
//
// The JSON field names below must stay in step with the schemas in
// internal/generate/prompts.go, which are what real providers are held to.
type Mock struct{}

// NewMock returns the offline provider.
func NewMock() *Mock { return &Mock{} }

// Name reports the provider.
func (m *Mock) Name() string { return "mock" }

// Complete dispatches on the request's task.
func (m *Mock) Complete(ctx context.Context, req Request) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	switch req.Task {
	case TaskResearch:
		return m.research(req)
	case TaskRender:
		return m.render(req)
	case TaskGrade:
		return m.grade(req)
	case TaskExplain:
		return m.explain(req)
	default:
		return "", fmt.Errorf("mock provider: unsupported task %q", req.Task)
	}
}

// mockAspects give generated facts a spread of subtopics so the weakness
// clustering and drill features have something to group by.
var mockAspects = []string{
	"origins", "key figures", "turning points", "terminology",
	"numbers and dates", "common misconceptions",
}

func (m *Mock) research(req Request) (string, error) {
	subject := req.Var("subject")
	if subject == "" {
		subject = "the subject"
	}
	count, _ := strconv.Atoi(req.Var("count"))
	if count <= 0 {
		count = 12
	}
	seed := hash(subject)

	facts := make([]map[string]any, 0, count)
	for i := 0; i < count; i++ {
		aspect := mockAspects[(int(seed)+i)%len(mockAspects)]
		facts = append(facts, map[string]any{
			"statement": fmt.Sprintf("Sample question %d about the %s of %s?", i+1, aspect, subject),
			"answer":    fmt.Sprintf("Sample answer %d (%s)", i+1, aspect),
			"explanation": fmt.Sprintf(
				"Synthetic content from the mock provider. A real run would explain "+
					"the %s of %s here. Configure TRIVIAL_LLM_API_KEY for genuine trivia.", aspect, subject),
			"tags":       []string{aspect},
			"difficulty": 1 + (i+int(seed))%5,
		})
	}
	return encode(map[string]any{"facts": facts})
}

func (m *Mock) render(req Request) (string, error) {
	statement := req.Var("statement")
	answer := req.Var("answer")
	if answer == "" {
		answer = "Sample answer"
	}
	seed := int(hash(statement))

	distractors := make([]string, 0, 3)
	for i := 1; i <= 3; i++ {
		distractors = append(distractors, fmt.Sprintf("Plausible but wrong option %d", (seed+i)%97))
	}
	// Place the correct answer at a stable but statement-dependent index so the
	// UI is not always testing the same slot.
	correct := seed % 4
	options := make([]string, 0, 4)
	options = append(options, distractors[:correct]...)
	options = append(options, answer)
	options = append(options, distractors[correct:]...)

	return encode(map[string]any{
		"open_prompt":    statement,
		"mc_prompt":      statement,
		"options":        options,
		"correct_option": correct,
	})
}

func (m *Mock) grade(req Request) (string, error) {
	canonical := req.Var("answer")
	response := req.Var("response")

	sim := Similarity(canonical, response)
	verdict, critique := "incorrect", "That does not match the expected answer."
	switch {
	case sim >= 0.8:
		verdict, critique = "correct", "Matches the expected answer."
	case sim >= 0.35:
		verdict, critique = "partial", "Partly right — some of the expected answer is missing."
	}
	return encode(map[string]any{"verdict": verdict, "critique": critique})
}

func (m *Mock) explain(req Request) (string, error) {
	return encode(map[string]any{
		"elaboration": fmt.Sprintf(
			"Mock elaboration. The fact under review is %q, whose answer is %q. "+
				"With a real provider configured, this is where a fuller explanation would appear.",
			req.Var("statement"), req.Var("answer")),
	})
}

func encode(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func hash(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}

// Similarity is Jaccard overlap over normalised word sets, with exact
// containment treated as a full match. It grades the mock provider's answers,
// and is also the word-overlap grader tutor uses when AI grading is switched
// off or the model is unreachable.
func Similarity(a, b string) float64 {
	aw, bw := words(a), words(b)
	if len(aw) == 0 || len(bw) == 0 {
		return 0
	}
	na, nb := strings.Join(keys(aw), " "), strings.Join(keys(bw), " ")
	if na == nb || strings.Contains(nb, na) {
		return 1
	}
	inter := 0
	for w := range aw {
		if bw[w] {
			inter++
		}
	}
	union := len(aw) + len(bw) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127)
	}) {
		out[f] = true
	}
	return out
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Sorted so the joined form is stable.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
