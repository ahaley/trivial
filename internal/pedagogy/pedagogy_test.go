package pedagogy

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/ahaley/trivial/internal/model"
	"github.com/ahaley/trivial/internal/store"
)

var now = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// testComposer uses a fixed seed so shuffling does not make tests flaky.
func testComposer() *Composer {
	return &Composer{Rand: rand.New(rand.NewPCG(1, 2))}
}

// bothModes is the usual case: a fact rendered in both ways.
func bothModes(id int64) map[model.Mode]int64 {
	return map[model.Mode]int64{model.ModeOpen: id * 10, model.ModeMC: id*10 + 1}
}

func newFact(id int64, topic int64, tags ...string) store.Candidate {
	return store.Candidate{FactID: id, TopicID: topic, Tags: tags, Difficulty: 3, Modes: bothModes(id)}
}

func seenFact(id, topic int64, stability float64, due time.Time, tags ...string) store.Candidate {
	return store.Candidate{
		FactID: id, TopicID: topic, Tags: tags, Difficulty: 3,
		Stability: stability, FSRSDiff: 5, DueAt: due,
		LastReview: due.Add(-time.Duration(stability * float64(24*time.Hour))),
		Reps:       2, Modes: bothModes(id),
	}
}

func TestPickModeFavoursRecallUntilStable(t *testing.T) {
	if got := PickMode(newFact(1, 1)); got != model.ModeOpen {
		t.Errorf("a new fact should be asked open-ended, got %q", got)
	}
	shaky := seenFact(2, 1, 3, now)
	if got := PickMode(shaky); got != model.ModeOpen {
		t.Errorf("a shaky fact should be asked open-ended, got %q", got)
	}
	solid := seenFact(3, 1, openStabilityDays+5, now)
	if got := PickMode(solid); got != model.ModeMC {
		t.Errorf("a well-known fact should be asked as multiple choice, got %q", got)
	}
}

func TestPickModeFallsBackToWhatExists(t *testing.T) {
	openOnly := newFact(1, 1)
	openOnly.Modes = map[model.Mode]int64{model.ModeOpen: 10}
	// Solid enough to prefer MC, but no MC rendering exists.
	openOnly.Reps, openOnly.Stability = 3, 100
	if got := PickMode(openOnly); got != model.ModeOpen {
		t.Errorf("should fall back to the only rendering, got %q", got)
	}

	mcOnly := newFact(2, 1)
	mcOnly.Modes = map[model.Mode]int64{model.ModeMC: 21}
	if got := PickMode(mcOnly); got != model.ModeMC {
		t.Errorf("should fall back to the only rendering, got %q", got)
	}
}

func TestComposeSchedulesDueBeforeNew(t *testing.T) {
	c := testComposer()
	candidates := []store.Candidate{
		newFact(1, 1, "a"),
		seenFact(2, 2, 10, now.Add(-48*time.Hour), "b"), // most overdue
		newFact(3, 3, "c"),
		seenFact(4, 4, 10, now.Add(-1*time.Hour), "d"),
	}

	items := c.Compose(candidates, 4, now)
	if len(items) != 4 {
		t.Fatalf("got %d items, want 4", len(items))
	}
	// The two due facts must both precede the two new ones.
	pos := map[int64]int{}
	for i, it := range items {
		pos[it.FactID] = i
	}
	for _, due := range []int64{2, 4} {
		for _, fresh := range []int64{1, 3} {
			if pos[due] > pos[fresh] {
				t.Errorf("due fact %d scheduled after new fact %d", due, fresh)
			}
		}
	}
	// Among the due, the more overdue one comes first.
	if pos[2] > pos[4] {
		t.Errorf("the more overdue fact should come first")
	}
}

func TestComposeRespectsSize(t *testing.T) {
	c := testComposer()
	var candidates []store.Candidate
	for i := int64(1); i <= 50; i++ {
		candidates = append(candidates, newFact(i, 1))
	}
	if got := len(c.Compose(candidates, 7, now)); got != 7 {
		t.Errorf("got %d items, want 7", got)
	}
	if got := c.Compose(candidates, 0, now); got != nil {
		t.Errorf("a zero-size session should be empty, got %d", len(got))
	}
	if got := c.Compose(nil, 10, now); got != nil {
		t.Errorf("no candidates should give no items, got %d", len(got))
	}
}

// Not-yet-due facts should only pad a session that would otherwise be short.
func TestComposeFallsBackToUpcomingOnlyWhenShort(t *testing.T) {
	c := testComposer()
	candidates := []store.Candidate{
		seenFact(1, 1, 10, now.Add(-time.Hour)),   // due
		seenFact(2, 1, 10, now.Add(72*time.Hour)), // not due
	}

	if items := c.Compose(candidates, 1, now); len(items) != 1 || items[0].FactID != 1 {
		t.Errorf("with room for one, only the due fact should be asked, got %+v", items)
	}
	if items := c.Compose(candidates, 5, now); len(items) != 2 {
		t.Errorf("with room to spare, the upcoming fact should pad the session, got %d", len(items))
	}
}

func TestInterleaveSeparatesTopicsAndTags(t *testing.T) {
	in := []store.Candidate{
		{FactID: 1, TopicID: 1, Tags: []string{"x"}},
		{FactID: 2, TopicID: 1, Tags: []string{"x"}},
		{FactID: 3, TopicID: 2, Tags: []string{"y"}},
		{FactID: 4, TopicID: 2, Tags: []string{"y"}},
	}
	out := interleave(in)
	if len(out) != len(in) {
		t.Fatalf("interleave changed the item count: %d -> %d", len(in), len(out))
	}
	adjacent := 0
	for i := 1; i < len(out); i++ {
		if out[i].TopicID == out[i-1].TopicID {
			adjacent++
		}
	}
	if adjacent > 0 {
		t.Errorf("with two topics of two, no two neighbours should share a topic; got %d clashes", adjacent)
	}
	// Every input must still be present exactly once.
	seen := map[int64]int{}
	for _, c := range out {
		seen[c.FactID]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("fact %d appears %d times", id, n)
		}
	}
}

// When every remaining candidate clashes, interleaving must still terminate
// and keep everything.
func TestInterleaveHandlesUnavoidableClashes(t *testing.T) {
	var in []store.Candidate
	for i := int64(1); i <= 6; i++ {
		in = append(in, store.Candidate{FactID: i, TopicID: 1, Tags: []string{"same"}})
	}
	out := interleave(in)
	if len(out) != 6 {
		t.Fatalf("got %d items, want 6", len(out))
	}
}

func TestComposeSkipsFactsWithNoRendering(t *testing.T) {
	c := testComposer()
	broken := newFact(1, 1)
	broken.Modes = map[model.Mode]int64{}
	items := c.Compose([]store.Candidate{broken, newFact(2, 1)}, 5, now)
	if len(items) != 1 || items[0].FactID != 2 {
		t.Errorf("a fact with no question should be skipped, got %+v", items)
	}
}
