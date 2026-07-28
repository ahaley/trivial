// Package generate runs the research → render → validate pipeline that turns a
// subject into a deck of trivia questions (SPEC.md §5).
package generate

import (
	"context"

	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/ahaley/trivial/internal/llm"
	"github.com/ahaley/trivial/internal/model"
	"github.com/ahaley/trivial/internal/store"
)

// Tuning constants for the pipeline.
const (
	// batchSize bounds how many facts are requested per research call. Smaller
	// batches keep responses well clear of output limits and let each batch see
	// what the previous ones covered.
	batchSize = 12
	// renderConcurrency bounds simultaneous render calls.
	renderConcurrency = 4
	// researchAttempts is how many times a batch is retried when it comes back
	// unusable (malformed JSON, or nothing new).
	researchAttempts = 2
	// dupThreshold is the word-overlap ratio above which two statements with
	// the same answer are treated as the same question asked twice. It can sit
	// this low because it is only ever consulted once the answers already
	// match, which is most of the evidence on its own.
	dupThreshold = 0.7
)

// Generator builds decks.
type Generator struct {
	store *store.Store
	llm   llm.Client
	log   *slog.Logger
}

// New returns a Generator.
func New(st *store.Store, client llm.Client, log *slog.Logger) *Generator {
	if log == nil {
		log = slog.Default()
	}
	return &Generator{store: st, llm: client, log: log}
}

// Run generates a topic's deck, recording progress and the terminal status on
// the topic row. It returns an error only for failures the caller should see;
// the topic is marked failed either way.
func (g *Generator) Run(ctx context.Context, topic model.Topic) error {
	err := g.run(ctx, topic)
	if err != nil {
		g.log.Error("generation failed", "topic", topic.ID, "subject", topic.Subject, "err", err)
		if serr := g.store.SetTopicStatus(topic.ID, model.StatusFailed, err.Error()); serr != nil {
			g.log.Error("could not record generation failure", "topic", topic.ID, "err", serr)
		}
		return err
	}
	if err := g.store.SetTopicStatus(topic.ID, model.StatusReady, ""); err != nil {
		return err
	}
	g.log.Info("generation complete", "topic", topic.ID, "subject", topic.Subject)
	return nil
}

func (g *Generator) run(ctx context.Context, topic model.Topic) error {
	target := topic.TargetCount
	if target <= 0 {
		target = 30
	}

	facts, err := g.research(ctx, topic, target)
	if err != nil {
		return err
	}
	if len(facts) == 0 {
		return errors.New("the model returned no usable facts for this subject")
	}
	if len(facts) < target {
		g.log.Warn("generated fewer facts than requested",
			"topic", topic.ID, "got", len(facts), "want", target)
	}

	return g.renderAll(ctx, topic, facts)
}

// research runs batched research calls until the target is met or a batch stops
// producing anything new, validating and de-duplicating as it goes.
func (g *Generator) research(ctx context.Context, topic model.Topic, target int) ([]model.Fact, error) {
	var accepted []model.Fact
	var covered []string

	g.progress(topic.ID, "researching", 5)

	for len(accepted) < target {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		want := min(batchSize, target-len(accepted))

		batch, err := g.researchBatch(ctx, topic, want, covered)
		if err != nil {
			// A later batch failing still leaves earlier ones usable.
			if len(accepted) > 0 {
				g.log.Warn("research batch failed; keeping what we have",
					"topic", topic.ID, "have", len(accepted), "err", err)
				break
			}
			return nil, err
		}

		added := 0
		for _, f := range batch {
			if isDuplicate(f, accepted) {
				continue
			}
			f.TopicID = topic.ID
			accepted = append(accepted, f)
			covered = append(covered, f.Statement)
			added++
			if len(accepted) >= target {
				break
			}
		}
		if added == 0 {
			// The subject is exhausted as far as the model is concerned.
			g.log.Info("research produced nothing new; stopping early",
				"topic", topic.ID, "facts", len(accepted))
			break
		}
		// Research spans 5–35% of the progress bar.
		g.progress(topic.ID, "researching", 5+int(30*float64(len(accepted))/float64(target)))
	}
	return accepted, nil
}

// researchBatch performs one research call and returns its validated facts.
func (g *Generator) researchBatch(ctx context.Context, topic model.Topic, want int, covered []string) ([]model.Fact, error) {
	type rawFact struct {
		Statement   string   `json:"statement"`
		Answer      string   `json:"answer"`
		Explanation string   `json:"explanation"`
		Tags        []string `json:"tags"`
		Difficulty  int      `json:"difficulty"`
	}
	type payload struct {
		Facts []rawFact `json:"facts"`
	}

	var lastErr error
	for attempt := 1; attempt <= researchAttempts; attempt++ {
		text, err := g.llm.Complete(ctx, llm.Request{
			Task:        llm.TaskResearch,
			System:      researchSystem,
			Prompt:      researchPrompt(topic.Subject, topic.Guidance, want, covered),
			Schema:      researchSchema,
			Temperature: 0.9, // variety matters more than determinism here
			// A dozen facts with explanations, plus whatever a reasoning model
			// spends thinking before it writes any of them.
			MaxTokens: 32768,
			Vars: map[string]string{
				"subject": topic.Subject,
				"count":   fmt.Sprint(want),
			},
		})
		if err != nil {
			return nil, fmt.Errorf("research call: %w", err)
		}

		var p payload
		if err := llm.DecodeJSON(text, &p); err != nil {
			lastErr = fmt.Errorf("research response was not valid JSON: %w", err)
			g.log.Warn("retrying malformed research response", "topic", topic.ID, "attempt", attempt)
			continue
		}

		out := make([]model.Fact, 0, len(p.Facts))
		for _, rf := range p.Facts {
			f := model.Fact{
				Statement:       strings.TrimSpace(rf.Statement),
				CanonicalAnswer: strings.TrimSpace(rf.Answer),
				Explanation:     strings.TrimSpace(rf.Explanation),
				Tags:            cleanTags(rf.Tags),
				Difficulty:      clampInt(rf.Difficulty, 1, 5),
			}
			if !validFact(f) {
				continue
			}
			out = append(out, f)
		}
		if len(out) == 0 {
			lastErr = errors.New("research response contained no usable facts")
			continue
		}
		return out, nil
	}
	return nil, lastErr
}

// renderAll renders each fact into its question modes and persists it.
func (g *Generator) renderAll(ctx context.Context, topic model.Topic, facts []model.Fact) error {
	g.progress(topic.ID, "writing questions", 35)

	var (
		mu       sync.Mutex
		done     int
		inserted int
		firstErr error
	)

	sem := make(chan struct{}, renderConcurrency)
	var wg sync.WaitGroup

	for _, f := range facts {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(f model.Fact) {
			defer wg.Done()
			defer func() { <-sem }()

			f.Questions = g.renderOne(ctx, topic, f)

			mu.Lock()
			defer mu.Unlock()
			if _, ok, err := g.store.InsertFact(f); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("save fact: %w", err)
				}
			} else if ok {
				inserted++
			}
			done++
			// Rendering spans 35–95% of the progress bar.
			g.progress(topic.ID, "writing questions", 35+int(60*float64(done)/float64(len(facts))))
		}(f)
	}
	wg.Wait()

	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if inserted == 0 {
		return errors.New("no questions could be saved for this subject")
	}
	g.progress(topic.ID, "finishing", 98)
	return nil
}

// renderOne produces a fact's question renderings. A fact always ends up with
// at least the open-ended rendering: that one can be built from the fact itself,
// so a failed or malformed render degrades rather than losing the fact.
func (g *Generator) renderOne(ctx context.Context, topic model.Topic, f model.Fact) []model.Question {
	open := model.Question{Mode: model.ModeOpen, Prompt: f.Statement, CorrectOption: -1}

	type payload struct {
		OpenPrompt    string   `json:"open_prompt"`
		MCPrompt      string   `json:"mc_prompt"`
		Options       []string `json:"options"`
		CorrectOption int      `json:"correct_option"`
	}

	text, err := g.llm.Complete(ctx, llm.Request{
		Task:        llm.TaskRender,
		System:      renderSystem,
		Prompt:      renderPrompt(topic.Subject, f),
		Schema:      renderSchema,
		Temperature: 0.7,
		// Writing four distractors is a short answer but a real reasoning task.
		MaxTokens: 8192,
		Vars: map[string]string{
			"statement": f.Statement,
			"answer":    f.CanonicalAnswer,
		},
	})
	if err != nil {
		g.log.Warn("render failed; keeping open-ended only", "topic", topic.ID, "err", err)
		return []model.Question{open}
	}

	var p payload
	if err := llm.DecodeJSON(text, &p); err != nil {
		g.log.Warn("render response was not valid JSON; keeping open-ended only", "topic", topic.ID)
		return []model.Question{open}
	}

	if s := strings.TrimSpace(p.OpenPrompt); s != "" {
		open.Prompt = s
	}

	out := []model.Question{open}
	if mc, ok := validMC(p.MCPrompt, f.Statement, p.Options, p.CorrectOption); ok {
		out = append(out, mc)
	} else {
		g.log.Warn("discarding invalid multiple-choice rendering",
			"topic", topic.ID, "statement", f.Statement)
	}
	return out
}

// validMC checks a multiple-choice rendering: four distinct non-empty options
// with an in-range correct index. Anything else is dropped rather than shown.
func validMC(prompt, fallbackPrompt string, options []string, correct int) (model.Question, bool) {
	if len(options) != 4 || correct < 0 || correct >= len(options) {
		return model.Question{}, false
	}
	seen := map[string]bool{}
	cleaned := make([]string, 0, 4)
	for _, o := range options {
		o = strings.TrimSpace(o)
		key := strings.ToLower(o)
		if o == "" || seen[key] {
			return model.Question{}, false
		}
		seen[key] = true
		cleaned = append(cleaned, o)
	}
	p := strings.TrimSpace(prompt)
	if p == "" {
		p = fallbackPrompt
	}
	return model.Question{
		Mode: model.ModeMC, Prompt: p, Options: cleaned, CorrectOption: correct,
	}, true
}

func (g *Generator) progress(topicID int64, stage string, pct int) {
	if err := g.store.UpdateTopicProgress(topicID, stage, clampInt(pct, 0, 99)); err != nil {
		g.log.Warn("could not record progress", "topic", topicID, "err", err)
	}
}

// validFact rejects facts too thin to quiz on.
func validFact(f model.Fact) bool {
	return len(f.Statement) >= 10 && f.CanonicalAnswer != ""
}

// isDuplicate reports whether f asks something already accepted.
//
// Identical statements are always duplicates. Merely similar statements are
// only duplicates when they also share an answer: questions that differ by one
// decisive word — "the first consul" against "the last consul" — overlap
// heavily as text but are exactly the contrast pairs worth keeping, and their
// answers are what tells them apart.
func isDuplicate(f model.Fact, accepted []model.Fact) bool {
	key := store.NormalizeKey(f.Statement)
	fw := wordSet(key)
	answer := store.NormalizeKey(f.CanonicalAnswer)

	for _, a := range accepted {
		akey := store.NormalizeKey(a.Statement)
		if key == akey {
			return true
		}
		if jaccard(fw, wordSet(akey)) >= dupThreshold &&
			answer == store.NormalizeKey(a.CanonicalAnswer) {
			return true
		}
	}
	return false
}

func wordSet(normalized string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, w := range strings.Fields(normalized) {
		out[w] = struct{}{}
	}
	return out
}

// jaccard is the intersection-over-union of two word sets.
func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for w := range a {
		if _, ok := b[w]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// cleanTags normalises subtopic tags to lowercase, drops blanks and duplicates,
// and keeps at most three.
func cleanTags(tags []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, 3)
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] || len(out) == 3 {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
