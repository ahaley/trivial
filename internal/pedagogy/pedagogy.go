// Package pedagogy composes quiz sessions (SPEC.md §7).
//
// It decides three things: which facts to ask, in which order, and in which
// mode. Scheduling itself lives in package fsrs; this package is the policy
// layer on top of it.
package pedagogy

import (
	"math/rand/v2"
	"sort"
	"time"

	"github.com/ahaley/trivial/internal/fsrs"
	"github.com/ahaley/trivial/internal/model"
	"github.com/ahaley/trivial/internal/store"
)

// openStabilityDays is the stability below which a fact is still considered
// shaky and is asked open-ended. Above it, recall is secure enough that a
// faster multiple-choice check is enough to keep it alive.
//
// Recall is a harder, more effective retrieval than recognition, so the bias is
// deliberately toward open-ended: multiple choice is the reward for mastery,
// not the default.
const openStabilityDays = 21

// Composer builds session queues.
type Composer struct {
	// Rand breaks ties and shuffles the new-fact pool so repeated sessions over
	// the same deck do not present questions in a memorised order.
	Rand *rand.Rand
}

// NewComposer returns a Composer seeded from the clock.
func NewComposer() *Composer {
	return &Composer{Rand: rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0x9E3779B97F4A7C15))}
}

// Compose selects and orders up to size questions from the candidate pool.
//
// Priority follows SPEC.md §7: everything already due comes first, most
// urgently forgotten first; then unseen facts; then, only if the session would
// otherwise be short, the facts closest to falling due. The result is then
// interleaved so consecutive questions come from different topics and
// subtopics.
func (c *Composer) Compose(candidates []store.Candidate, size int, now time.Time) []store.QueueItem {
	if size <= 0 || len(candidates) == 0 {
		return nil
	}

	var due, fresh, upcoming []store.Candidate
	for _, cand := range candidates {
		switch {
		case cand.New():
			fresh = append(fresh, cand)
		case !cand.DueAt.After(now):
			due = append(due, cand)
		default:
			upcoming = append(upcoming, cand)
		}
	}

	// Most urgent first: the lowest predicted recall is the fact closest to
	// being lost, which is where a review is worth most.
	byUrgency := func(list []store.Candidate) {
		sort.SliceStable(list, func(i, j int) bool {
			ri, rj := retrievability(list[i], now), retrievability(list[j], now)
			if ri != rj {
				return ri < rj
			}
			return list[i].FactID < list[j].FactID
		})
	}
	byUrgency(due)
	byUrgency(upcoming)

	// Unseen facts are shuffled, then ordered easiest-first so a new deck opens
	// gently rather than on its hardest question.
	c.shuffle(fresh)
	sort.SliceStable(fresh, func(i, j int) bool { return fresh[i].Difficulty < fresh[j].Difficulty })

	picked := make([]store.Candidate, 0, size)
	for _, group := range [][]store.Candidate{due, fresh, upcoming} {
		for _, cand := range group {
			if len(picked) == size {
				break
			}
			picked = append(picked, cand)
		}
	}

	picked = interleave(picked)

	items := make([]store.QueueItem, 0, len(picked))
	for _, cand := range picked {
		mode := PickMode(cand)
		qid, ok := cand.Modes[mode]
		if !ok {
			continue
		}
		items = append(items, store.QueueItem{QuestionID: qid, FactID: cand.FactID})
	}
	return items
}

// PickMode chooses how to ask a fact: recall for anything not yet secure,
// recognition for facts that are. Falls back to whichever rendering exists.
func PickMode(c store.Candidate) model.Mode {
	preferred := model.ModeOpen
	if !c.New() && c.Stability >= openStabilityDays {
		preferred = model.ModeMC
	}
	if _, ok := c.Modes[preferred]; ok {
		return preferred
	}
	if preferred == model.ModeOpen {
		return model.ModeMC
	}
	return model.ModeOpen
}

// retrievability is a candidate's predicted recall probability now.
func retrievability(c store.Candidate, now time.Time) float64 {
	return fsrs.State{
		Stability:  c.Stability,
		Difficulty: c.FSRSDiff,
		LastReview: c.LastReview,
		Reps:       c.Reps,
	}.Retrievability(now)
}

// interleave reorders a queue so that adjacent questions come from different
// topics and different subtopics where possible. Mixing categories rather than
// blocking by them improves discrimination and long-term retention, and it is
// what makes a cross-topic review session more than a concatenation.
//
// The relative priority established by Compose is preserved as far as the
// constraint allows: each step takes the highest-priority candidate that does
// not clash with the previous one, and falls back to the plain next one when
// every remaining candidate clashes.
func interleave(in []store.Candidate) []store.Candidate {
	if len(in) < 3 {
		return in
	}
	remaining := make([]store.Candidate, len(in))
	copy(remaining, in)

	out := make([]store.Candidate, 0, len(in))
	out = append(out, remaining[0])
	remaining = remaining[1:]

	for len(remaining) > 0 {
		prev := out[len(out)-1]
		pick := 0
		for i, cand := range remaining {
			if cand.TopicID != prev.TopicID && !sharesTag(cand, prev) {
				pick = i
				break
			}
		}
		out = append(out, remaining[pick])
		remaining = append(remaining[:pick], remaining[pick+1:]...)
	}
	return out
}

func sharesTag(a, b store.Candidate) bool {
	if len(a.Tags) == 0 || len(b.Tags) == 0 {
		return false
	}
	set := make(map[string]struct{}, len(b.Tags))
	for _, t := range b.Tags {
		set[t] = struct{}{}
	}
	for _, t := range a.Tags {
		if _, ok := set[t]; ok {
			return true
		}
	}
	return false
}

func (c *Composer) shuffle(list []store.Candidate) {
	if c.Rand == nil || len(list) < 2 {
		return
	}
	c.Rand.Shuffle(len(list), func(i, j int) { list[i], list[j] = list[j], list[i] })
}
