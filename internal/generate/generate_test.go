package generate

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/ahaley/trivial/internal/llm"
	"github.com/ahaley/trivial/internal/model"
	"github.com/ahaley/trivial/internal/store"
)

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

func fact(statement, answer string) model.Fact {
	return model.Fact{Statement: statement, CanonicalAnswer: answer}
}

func TestIsDuplicate(t *testing.T) {
	accepted := []model.Fact{
		fact("Who was the first consul of Rome?", "Brutus"),
		fact("In which year did the Republic fall?", "27 BC"),
	}

	tests := []struct {
		name string
		f    model.Fact
		want bool
	}{
		{"identical", fact("Who was the first consul of Rome?", "Brutus"), true},
		{"repunctuated", fact("who was the first consul of rome???", "Brutus"), true},
		{"contrast pair", fact("Who was the last consul of Rome?", "Ovinius"), false},
		{"unrelated", fact("Which battle opened the Punic Wars?", "Mylae"), false},
		// A heavy rewording shares too few words for overlap to catch, even
		// with the same answer. Prompting the model with what it has already
		// covered is the primary defence; this check is the backstop.
		{"heavily reworded", fact("Who was Rome's very first consul?", "Brutus"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDuplicate(tc.f, accepted); got != tc.want {
				t.Errorf("isDuplicate = %v, want %v", got, tc.want)
			}
		})
	}
}

// The decisive case: near-identical wording with a different answer is a
// contrast pair worth keeping, not a duplicate.
func TestIsDuplicateKeepsContrastPairs(t *testing.T) {
	accepted := []model.Fact{fact("In which year did the war begin?", "1914")}
	end := fact("In which year did the war end?", "1918")
	if isDuplicate(end, accepted) {
		t.Error("questions differing only by a decisive word should both be kept")
	}
	same := fact("In which year did the war start?", "1914")
	if !isDuplicate(same, accepted) {
		t.Error("the same question reworded, with the same answer, is a duplicate")
	}
}

func TestValidMC(t *testing.T) {
	good := []string{"Mylae", "Cannae", "Zama", "Actium"}

	q, ok := validMC("Which battle?", "fallback", good, 0)
	if !ok {
		t.Fatal("a well-formed rendering should be accepted")
	}
	if q.Mode != model.ModeMC || q.Prompt != "Which battle?" || len(q.Options) != 4 || q.CorrectOption != 0 {
		t.Errorf("unexpected question: %+v", q)
	}

	// An empty prompt falls back to the fact's own statement.
	if q, _ := validMC("  ", "fallback", good, 1); q.Prompt != "fallback" {
		t.Errorf("prompt = %q, want the fallback", q.Prompt)
	}

	bad := []struct {
		name    string
		options []string
		correct int
	}{
		{"too few options", []string{"a", "b", "c"}, 0},
		{"too many options", []string{"a", "b", "c", "d", "e"}, 0},
		{"index out of range", good, 4},
		{"negative index", good, -1},
		{"duplicate options", []string{"Mylae", "Mylae", "Zama", "Actium"}, 0},
		{"duplicate ignoring case", []string{"Mylae", "mylae", "Zama", "Actium"}, 0},
		{"blank option", []string{"Mylae", "", "Zama", "Actium"}, 0},
		{"no options", nil, 0},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := validMC("Which battle?", "fallback", tc.options, tc.correct); ok {
				t.Error("should have been rejected")
			}
		})
	}
}

func TestValidFact(t *testing.T) {
	if !validFact(fact("Who was Cato the Elder?", "a censor")) {
		t.Error("a complete fact should be valid")
	}
	if validFact(fact("Too short", "x")) {
		t.Error("a statement under ten characters is not a question")
	}
	if validFact(fact("Who was Cato the Elder?", "")) {
		t.Error("a fact with no answer cannot be graded")
	}
}

func TestCleanTags(t *testing.T) {
	got := cleanTags([]string{"  Naval Battles ", "naval battles", "", "Chronology", "People", "Extra"})
	want := []string{"naval battles", "chronology", "people"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tag %d = %q, want %q", i, got[i], want[i])
		}
	}
	if got := cleanTags(nil); len(got) != 0 {
		t.Errorf("nil tags should give none, got %v", got)
	}
}

func TestJaccard(t *testing.T) {
	a := wordSet("who was the first consul")
	if got := jaccard(a, a); got != 1 {
		t.Errorf("a set against itself = %v, want 1", got)
	}
	if got := jaccard(a, wordSet("entirely different words here")); got != 0 {
		t.Errorf("disjoint sets = %v, want 0", got)
	}
	if got := jaccard(a, nil); got != 0 {
		t.Errorf("an empty set = %v, want 0", got)
	}
}

// End to end against the offline provider: a topic goes in, a usable deck
// comes out and the topic is marked ready.
func TestGenerateEndToEnd(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "gen.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	topic, err := st.CreateTopic("The Roman Republic", "", 8)
	if err != nil {
		t.Fatalf("create topic: %v", err)
	}

	g := New(st, llm.NewMock(), quiet())
	if err := g.Run(context.Background(), topic); err != nil {
		t.Fatalf("generate: %v", err)
	}

	got, err := st.GetTopic(topic.ID)
	if err != nil {
		t.Fatalf("get topic: %v", err)
	}
	if got.Status != model.StatusReady {
		t.Fatalf("status = %q (%s), want ready", got.Status, got.Error)
	}
	if got.Progress != 100 {
		t.Errorf("progress = %d, want 100", got.Progress)
	}

	facts, err := st.ListFacts(topic.ID)
	if err != nil {
		t.Fatalf("list facts: %v", err)
	}
	if len(facts) == 0 || len(facts) > 8 {
		t.Fatalf("got %d facts, want between 1 and 8", len(facts))
	}
	for _, f := range facts {
		if f.CanonicalAnswer == "" || f.Statement == "" {
			t.Errorf("fact %d is incomplete: %+v", f.ID, f)
		}
		if f.Difficulty < 1 || f.Difficulty > 5 {
			t.Errorf("fact %d difficulty %d out of range", f.ID, f.Difficulty)
		}
		modes := map[model.Mode]bool{}
		for _, q := range f.Questions {
			modes[q.Mode] = true
			if q.Mode == model.ModeMC {
				if len(q.Options) != 4 {
					t.Errorf("fact %d: %d options, want 4", f.ID, len(q.Options))
				}
				if q.CorrectOption < 0 || q.CorrectOption >= len(q.Options) {
					t.Errorf("fact %d: correct option %d out of range", f.ID, q.CorrectOption)
				}
				// The correct option must actually be the canonical answer.
				if q.Options[q.CorrectOption] != f.CanonicalAnswer {
					t.Errorf("fact %d: correct option %q is not the answer %q",
						f.ID, q.Options[q.CorrectOption], f.CanonicalAnswer)
				}
			}
		}
		// Every fact must at least be askable open-ended.
		if !modes[model.ModeOpen] {
			t.Errorf("fact %d has no open-ended rendering", f.ID)
		}
	}
}

// A provider that always fails must leave the topic marked failed, with the
// reason recorded rather than swallowed.
func TestGenerateRecordsFailure(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "gen.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	topic, _ := st.CreateTopic("Anything", "", 5)
	g := New(st, &failingClient{}, quiet())
	if err := g.Run(context.Background(), topic); err == nil {
		t.Fatal("expected an error")
	}

	got, _ := st.GetTopic(topic.ID)
	if got.Status != model.StatusFailed {
		t.Errorf("status = %q, want failed", got.Status)
	}
	if got.Error == "" {
		t.Error("the failure reason should be recorded on the topic")
	}
}

type failingClient struct{}

func (failingClient) Name() string { return "failing" }
func (failingClient) Complete(context.Context, llm.Request) (string, error) {
	return "", io.ErrUnexpectedEOF
}
