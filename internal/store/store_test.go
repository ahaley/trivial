package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ahaley/trivial/internal/model"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// readyTopic creates a topic with n facts, each rendered in both modes.
func readyTopic(t *testing.T, st *Store, subject string, n int) model.Topic {
	t.Helper()
	topic, err := st.CreateTopic(subject, "", n)
	if err != nil {
		t.Fatalf("create topic: %v", err)
	}
	for i := 1; i <= n; i++ {
		f := model.Fact{
			TopicID:         topic.ID,
			Statement:       subject + " question " + string(rune('a'+i-1)),
			CanonicalAnswer: "answer " + string(rune('a'+i-1)),
			Explanation:     "because",
			Tags:            []string{"tag" + string(rune('a'+i-1))},
			Difficulty:      3,
			Questions: []model.Question{
				{Mode: model.ModeOpen, Prompt: "open prompt", CorrectOption: -1},
				{Mode: model.ModeMC, Prompt: "mc prompt", Options: []string{"a", "b", "c", "d"}, CorrectOption: 1},
			},
		}
		if _, ok, err := st.InsertFact(f); err != nil || !ok {
			t.Fatalf("insert fact %d: ok=%v err=%v", i, ok, err)
		}
	}
	if err := st.SetTopicStatus(topic.ID, model.StatusReady, ""); err != nil {
		t.Fatalf("set ready: %v", err)
	}
	return topic
}

func TestMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	for i := 0; i < 3; i++ {
		st, err := Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		st.Close()
	}
}

func TestNormalizeKey(t *testing.T) {
	cases := map[string]string{
		"  Who was Cato?  ":           "who was cato",
		"WHO   was\tCato???":          "who was cato",
		"Who was Cato":                "who was cato",
		"":                            "",
		"...":                         "",
		"Année 1789 — la Révolution?": "année 1789 la révolution",
	}
	for in, want := range cases {
		if got := NormalizeKey(in); got != want {
			t.Errorf("NormalizeKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInsertFactRejectsDuplicateStatements(t *testing.T) {
	st := openTest(t)
	topic, _ := st.CreateTopic("Rome", "", 5)

	f := model.Fact{
		TopicID: topic.ID, Statement: "Who was Cato?", CanonicalAnswer: "the Elder",
		Questions: []model.Question{{Mode: model.ModeOpen, Prompt: "Who was Cato?", CorrectOption: -1}},
	}
	if _, ok, err := st.InsertFact(f); err != nil || !ok {
		t.Fatalf("first insert: ok=%v err=%v", ok, err)
	}
	// Same question, differently punctuated: the normalised key must catch it.
	f.Statement = "who was cato???"
	if _, ok, err := st.InsertFact(f); err != nil || ok {
		t.Errorf("duplicate insert: ok=%v err=%v, want ok=false", ok, err)
	}

	facts, err := st.ListFacts(topic.ID)
	if err != nil || len(facts) != 1 {
		t.Fatalf("got %d facts, want 1 (err %v)", len(facts), err)
	}
}

func TestCandidatesOnlyIncludesAnswerableFacts(t *testing.T) {
	st := openTest(t)
	ready := readyTopic(t, st, "Rome", 3)

	// A topic still generating must not be quizzed on.
	pending, _ := st.CreateTopic("Pending", "", 3)
	st.InsertFact(model.Fact{
		TopicID: pending.ID, Statement: "not ready yet", CanonicalAnswer: "x",
		Questions: []model.Question{{Mode: model.ModeOpen, Prompt: "p", CorrectOption: -1}},
	})

	// A fact with no rendering cannot be asked.
	st.InsertFact(model.Fact{TopicID: ready.ID, Statement: "orphan fact here", CanonicalAnswer: "x"})

	cands, err := st.Candidates(nil, "")
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(cands) != 3 {
		t.Fatalf("got %d candidates, want 3", len(cands))
	}
	for _, c := range cands {
		if c.TopicID != ready.ID {
			t.Errorf("candidate from topic %d leaked in", c.TopicID)
		}
		if len(c.Modes) != 2 {
			t.Errorf("fact %d has %d modes, want 2", c.FactID, len(c.Modes))
		}
		if !c.New() {
			t.Errorf("fact %d should be new", c.FactID)
		}
	}
}

func TestCandidatesFilterByTopicAndTag(t *testing.T) {
	st := openTest(t)
	a := readyTopic(t, st, "Rome", 3)
	readyTopic(t, st, "Tennis", 2)

	all, _ := st.Candidates(nil, "")
	if len(all) != 5 {
		t.Errorf("unfiltered: got %d, want 5", len(all))
	}
	byTopic, _ := st.Candidates(&a.ID, "")
	if len(byTopic) != 3 {
		t.Errorf("by topic: got %d, want 3", len(byTopic))
	}
	byTag, _ := st.Candidates(nil, "taga")
	if len(byTag) != 2 { // one "taga" fact in each topic
		t.Errorf("by tag: got %d, want 2", len(byTag))
	}
	// Tag matching is case-insensitive.
	if upper, _ := st.Candidates(nil, "TAGA"); len(upper) != 2 {
		t.Errorf("tag matching should ignore case, got %d", len(upper))
	}
	if none, _ := st.Candidates(nil, "nosuchtag"); len(none) != 0 {
		t.Errorf("unknown tag: got %d, want 0", len(none))
	}
}

func TestSessionQueueDrainsInOrder(t *testing.T) {
	st := openTest(t)
	topic := readyTopic(t, st, "Rome", 3)
	cands, _ := st.Candidates(&topic.ID, "")

	items := make([]QueueItem, 0, len(cands))
	for _, c := range cands {
		items = append(items, QueueItem{QuestionID: c.Modes[model.ModeOpen], FactID: c.FactID})
	}
	sess, err := st.CreateSession(&topic.ID, model.KindTopic, "", items)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	for i := range items {
		itemID, q, f, err := st.NextItem(sess.ID)
		if err != nil {
			t.Fatalf("next %d: %v", i, err)
		}
		if q.FactID != f.ID {
			t.Errorf("question and fact disagree: %d vs %d", q.FactID, f.ID)
		}
		if got := items[i].FactID; f.ID != got {
			t.Errorf("item %d: got fact %d, want %d", i, f.ID, got)
		}
		if err := st.MarkAnswered(itemID); err != nil {
			t.Fatalf("mark answered: %v", err)
		}
	}
	if _, _, _, err := st.NextItem(sess.ID); err != ErrQueueEmpty {
		t.Errorf("drained queue should report ErrQueueEmpty, got %v", err)
	}
}

// The bound on relearning is what keeps a session with repeated misses from
// running forever.
func TestAppendRelearnIsBounded(t *testing.T) {
	st := openTest(t)
	topic := readyTopic(t, st, "Rome", 1)
	cands, _ := st.Candidates(&topic.ID, "")
	c := cands[0]
	qid := c.Modes[model.ModeOpen]

	sess, _ := st.CreateSession(&topic.ID, model.KindTopic, "",
		[]QueueItem{{QuestionID: qid, FactID: c.FactID}})

	// While the fact is still queued, no repeat is added.
	if queued, err := st.AppendRelearn(sess.ID, qid, c.FactID); err != nil || queued {
		t.Errorf("queued=%v err=%v, want queued=false while the fact is pending", queued, err)
	}

	// Answer it, miss it: one repeat is allowed.
	itemID, _, _, _ := st.NextItem(sess.ID)
	st.MarkAnswered(itemID)
	if queued, err := st.AppendRelearn(sess.ID, qid, c.FactID); err != nil || !queued {
		t.Fatalf("queued=%v err=%v, want the first repeat to be queued", queued, err)
	}

	// Miss the repeat too: no further repeats, or the session never ends.
	itemID, _, _, _ = st.NextItem(sess.ID)
	st.MarkAnswered(itemID)
	if queued, err := st.AppendRelearn(sess.ID, qid, c.FactID); err != nil || queued {
		t.Errorf("queued=%v err=%v, want repeats to stop at MaxRelearnPerFact", queued, err)
	}

	if _, _, _, err := st.NextItem(sess.ID); err != ErrQueueEmpty {
		t.Errorf("session should now be empty, got %v", err)
	}
	answered, total, _ := st.Progress(sess.ID)
	if answered != 2 || total != 2 {
		t.Errorf("progress = %d/%d, want 2/2", answered, total)
	}
}

func TestReviewStateRoundTrip(t *testing.T) {
	st := openTest(t)
	topic := readyTopic(t, st, "Rome", 1)
	facts, _ := st.ListFacts(topic.ID)
	id := facts[0].ID

	// An unseen fact reads back as a zero state rather than an error.
	got, err := st.GetReview(id)
	if err != nil || got.Reps != 0 {
		t.Fatalf("unseen review: %+v err=%v", got, err)
	}

	due := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Millisecond)
	want := model.Review{
		FactID: id, Stability: 12.5, Difficulty: 6.25,
		DueAt: due, LastReview: due.Add(-48 * time.Hour), Reps: 3, Lapses: 1,
	}
	if err := st.SaveReview(want); err != nil {
		t.Fatalf("save review: %v", err)
	}
	got, err = st.GetReview(id)
	if err != nil {
		t.Fatalf("get review: %v", err)
	}
	if got.Stability != want.Stability || got.Reps != want.Reps || got.Lapses != want.Lapses {
		t.Errorf("round trip mismatch: got %+v want %+v", got, want)
	}
	if !got.DueAt.Equal(want.DueAt) {
		t.Errorf("due at = %v, want %v", got.DueAt, want.DueAt)
	}

	// Saving again must update in place, not duplicate.
	want.Stability = 30
	if err := st.SaveReview(want); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	if got, _ := st.GetReview(id); got.Stability != 30 {
		t.Errorf("upsert did not update: %v", got.Stability)
	}
}

func TestSummaryCountsAndMastery(t *testing.T) {
	st := openTest(t)
	topic := readyTopic(t, st, "Rome", 4)
	facts, _ := st.ListFacts(topic.ID)

	sum, err := st.TopicSummaryByID(topic.ID)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if sum.FactCount != 4 || sum.NewCount != 4 || sum.DueCount != 0 || sum.Mastery != 0 {
		t.Errorf("fresh deck summary wrong: %+v", sum)
	}

	// One fact just reviewed and far from due; one overdue.
	now := time.Now()
	st.SaveReview(model.Review{
		FactID: facts[0].ID, Stability: 20, Difficulty: 5,
		DueAt: now.Add(20 * 24 * time.Hour), LastReview: now, Reps: 1,
	})
	st.SaveReview(model.Review{
		FactID: facts[1].ID, Stability: 1, Difficulty: 5,
		DueAt: now.Add(-24 * time.Hour), LastReview: now.Add(-10 * 24 * time.Hour), Reps: 1,
	})

	sum, _ = st.TopicSummaryByID(topic.ID)
	if sum.NewCount != 2 {
		t.Errorf("new count = %d, want 2", sum.NewCount)
	}
	if sum.DueCount != 1 {
		t.Errorf("due count = %d, want 1", sum.DueCount)
	}
	// The just-reviewed fact contributes ~1.0 and the overdue one ~0.55 — the
	// power-law curve has a long tail, so even ten stability-lengths late a
	// fact is not written off — over four facts, two of them never seen.
	if sum.Mastery <= 0.35 || sum.Mastery >= 0.42 {
		t.Errorf("mastery = %v, want about 0.39", sum.Mastery)
	}
	if sum.Stability <= 10 || sum.Stability > 11 {
		t.Errorf("mean stability over seen facts = %v, want 10.5", sum.Stability)
	}
}

func TestDeleteTopicCascades(t *testing.T) {
	st := openTest(t)
	topic := readyTopic(t, st, "Rome", 2)
	facts, _ := st.ListFacts(topic.ID)

	sess, _ := st.CreateSession(&topic.ID, model.KindTopic, "",
		[]QueueItem{{QuestionID: facts[0].Questions[0].ID, FactID: facts[0].ID}})
	st.RecordAttempt(model.Attempt{
		SessionID: sess.ID, QuestionID: facts[0].Questions[0].ID, FactID: facts[0].ID,
		Grade: model.GradeCorrect, Rating: 3,
	})
	st.SaveReview(model.Review{FactID: facts[0].ID, Stability: 5, Difficulty: 5,
		DueAt: time.Now(), LastReview: time.Now(), Reps: 1})

	if err := st.DeleteTopic(topic.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, table := range []string{"facts", "questions", "sessions", "attempts", "review_state", "session_items"} {
		var n int
		if err := st.DB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s still has %d rows after deleting the topic", table, n)
		}
	}
	if err := st.DeleteTopic(topic.ID); err != ErrNotFound {
		t.Errorf("deleting a missing topic should be ErrNotFound, got %v", err)
	}
}

func TestStatsAggregates(t *testing.T) {
	st := openTest(t)
	topic := readyTopic(t, st, "Rome", 3)
	facts, _ := st.ListFacts(topic.ID)

	sess, _ := st.CreateSession(&topic.ID, model.KindTopic, "", nil)
	grades := []model.Grade{model.GradeCorrect, model.GradeIncorrect, model.GradeCorrect,
		model.GradeIncorrect, model.GradeIncorrect, model.GradeCorrect}
	for i, g := range grades {
		f := facts[i%len(facts)]
		if _, err := st.RecordAttempt(model.Attempt{
			SessionID: sess.ID, QuestionID: f.Questions[0].ID, FactID: f.ID,
			Grade: g, Rating: 3,
		}); err != nil {
			t.Fatalf("record attempt: %v", err)
		}
	}

	stats, err := st.Stats(30)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Totals.Attempts != 6 {
		t.Errorf("attempts = %d, want 6", stats.Totals.Attempts)
	}
	if stats.Totals.Accuracy != 0.5 {
		t.Errorf("accuracy = %v, want 0.5", stats.Totals.Accuracy)
	}
	if stats.Totals.Facts != 3 || stats.Totals.Topics != 1 {
		t.Errorf("totals wrong: %+v", stats.Totals)
	}
	if len(stats.History) != 30 {
		t.Errorf("history has %d days, want 30", len(stats.History))
	}
	if last := stats.History[len(stats.History)-1]; last.Attempts != 6 {
		t.Errorf("today should hold all 6 attempts, got %d", last.Attempts)
	}
	if stats.Totals.StreakDay != 1 {
		t.Errorf("streak = %d, want 1", stats.Totals.StreakDay)
	}
	// Every tag has 2 attempts, below the 3-attempt floor for weak-tag ranking.
	if len(stats.WeakTags) != 0 {
		t.Errorf("thin samples should not be ranked as weaknesses, got %+v", stats.WeakTags)
	}
}

func TestRankWeakestOrdersByAccuracyThenSample(t *testing.T) {
	tally := map[string]*TagScore{
		"good":  {Tag: "good", Attempts: 10, Correct: 9},
		"bad":   {Tag: "bad", Attempts: 10, Correct: 1},
		"tied":  {Tag: "tied", Attempts: 4, Correct: 2},
		"tied2": {Tag: "tied2", Attempts: 20, Correct: 10},
	}
	got := rankWeakest(tally, 3)
	if len(got) != 3 {
		t.Fatalf("got %d, want 3", len(got))
	}
	if got[0].Tag != "bad" {
		t.Errorf("worst tag should be first, got %q", got[0].Tag)
	}
	// Equal accuracy: the larger sample is the more trustworthy weakness.
	if got[1].Tag != "tied2" || got[2].Tag != "tied" {
		t.Errorf("ties should favour the bigger sample, got %q then %q", got[1].Tag, got[2].Tag)
	}
	if got[0].Accuracy != 0.1 {
		t.Errorf("accuracy not computed: %v", got[0].Accuracy)
	}
}

func TestResetStuckGenerations(t *testing.T) {
	st := openTest(t)
	topic, _ := st.CreateTopic("Interrupted", "", 10)

	if err := st.ResetStuckGenerations(); err != nil {
		t.Fatalf("reset: %v", err)
	}
	got, _ := st.GetTopic(topic.ID)
	if got.Status != model.StatusFailed {
		t.Errorf("status = %q, want failed", got.Status)
	}
	if got.Error == "" {
		t.Error("a reset topic should explain itself")
	}
}

func TestGetTopicNotFound(t *testing.T) {
	st := openTest(t)
	if _, err := st.GetTopic(404); err != ErrNotFound {
		t.Errorf("got %v, want ErrNotFound", err)
	}
	if _, err := st.GetSession(404); err != ErrNotFound {
		t.Errorf("got %v, want ErrNotFound", err)
	}
	if _, err := st.GetAttemptContext(404); err != ErrNotFound {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}
