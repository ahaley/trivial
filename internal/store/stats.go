package store

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/ahaley/trivial/internal/fsrs"
	"github.com/ahaley/trivial/internal/model"
)

// Stats is the review dashboard payload (SPEC.md §4.3).
type Stats struct {
	Totals    Totals      `json:"totals"`
	History   []DayStat   `json:"history"`
	WeakTags  []TagScore  `json:"weak_tags"`
	DueQueue  []DueBucket `json:"due_queue"`
	TopicRoll []TopicRoll `json:"topics"`
}

// Totals are the headline numbers.
type Totals struct {
	Topics    int     `json:"topics"`
	Facts     int     `json:"facts"`
	Seen      int     `json:"seen"`
	DueNow    int     `json:"due_now"`
	NewFacts  int     `json:"new_facts"`
	Mastery   float64 `json:"mastery"`
	Attempts  int     `json:"attempts"`
	Accuracy  float64 `json:"accuracy"`
	StreakDay int     `json:"streak_days"`
}

// DayStat is one day of review activity.
type DayStat struct {
	Date     string  `json:"date"`
	Attempts int     `json:"attempts"`
	Correct  int     `json:"correct"`
	Accuracy float64 `json:"accuracy"`
}

// DueBucket is how many facts come due on a given day.
type DueBucket struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
	// Overdue marks the bucket holding everything already due.
	Overdue bool `json:"overdue,omitempty"`
}

// TopicRoll is a per-topic row on the dashboard.
type TopicRoll struct {
	TopicID int64   `json:"topic_id"`
	Subject string  `json:"subject"`
	Facts   int     `json:"facts"`
	Due     int     `json:"due"`
	Mastery float64 `json:"mastery"`
}

// Stats computes the dashboard. historyDays bounds the activity chart.
func (s *Store) Stats(historyDays int) (Stats, error) {
	if historyDays <= 0 {
		historyDays = 30
	}
	var st Stats
	now := time.Now()

	summaries, err := s.ListTopics()
	if err != nil {
		return st, err
	}
	var masterySum float64
	for _, sum := range summaries {
		st.Totals.Topics++
		st.Totals.Facts += sum.FactCount
		st.Totals.DueNow += sum.DueCount
		st.Totals.NewFacts += sum.NewCount
		masterySum += sum.Mastery * float64(sum.FactCount)
		if sum.FactCount > 0 {
			st.TopicRoll = append(st.TopicRoll, TopicRoll{
				TopicID: sum.ID, Subject: sum.Subject, Facts: sum.FactCount,
				Due: sum.DueCount, Mastery: sum.Mastery,
			})
		}
	}
	if st.Totals.Facts > 0 {
		st.Totals.Mastery = masterySum / float64(st.Totals.Facts)
	}
	st.Totals.Seen = st.Totals.Facts - st.Totals.NewFacts

	if err := s.db.QueryRow(`SELECT COUNT(*) FROM attempts`).Scan(&st.Totals.Attempts); err != nil {
		return st, fmt.Errorf("count attempts: %w", err)
	}
	if st.Totals.Attempts > 0 {
		var correct int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM attempts WHERE grade = ?`,
			string(model.GradeCorrect)).Scan(&correct); err != nil {
			return st, err
		}
		st.Totals.Accuracy = float64(correct) / float64(st.Totals.Attempts)
	}

	if st.History, err = s.history(now, historyDays); err != nil {
		return st, err
	}
	st.Totals.StreakDay = streak(st.History, now)

	if st.WeakTags, err = s.weakTags(5); err != nil {
		return st, err
	}
	if st.DueQueue, err = s.dueQueue(now, 14); err != nil {
		return st, err
	}
	return st, nil
}

// history buckets attempts by calendar day, filling in days with no activity.
func (s *Store) history(now time.Time, days int) ([]DayStat, error) {
	since := now.AddDate(0, 0, -(days - 1)).Truncate(24 * time.Hour)
	rows, err := s.db.Query(`SELECT created_at, grade FROM attempts WHERE created_at >= ?`,
		formatTime(since))
	if err != nil {
		return nil, fmt.Errorf("attempt history: %w", err)
	}
	defer rows.Close()

	byDay := map[string]*DayStat{}
	for rows.Next() {
		var created, grade string
		if err := rows.Scan(&created, &grade); err != nil {
			return nil, err
		}
		// Bucket in local time so "today" matches the user's day.
		key := mustParseTime(created).Local().Format("2006-01-02")
		d, ok := byDay[key]
		if !ok {
			d = &DayStat{Date: key}
			byDay[key] = d
		}
		d.Attempts++
		if model.Grade(grade) == model.GradeCorrect {
			d.Correct++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]DayStat, 0, days)
	for i := days - 1; i >= 0; i-- {
		key := now.AddDate(0, 0, -i).Format("2006-01-02")
		if d, ok := byDay[key]; ok {
			if d.Attempts > 0 {
				d.Accuracy = float64(d.Correct) / float64(d.Attempts)
			}
			out = append(out, *d)
		} else {
			out = append(out, DayStat{Date: key})
		}
	}
	return out, nil
}

// streak counts consecutive days with activity ending today (or yesterday, so
// a streak isn't lost until a full day has been missed).
func streak(history []DayStat, now time.Time) int {
	active := map[string]bool{}
	for _, d := range history {
		if d.Attempts > 0 {
			active[d.Date] = true
		}
	}
	start := 0
	if !active[now.Format("2006-01-02")] {
		start = 1
	}
	n := 0
	for i := start; ; i++ {
		if !active[now.AddDate(0, 0, -i).Format("2006-01-02")] {
			return n
		}
		n++
	}
}

// weakTags ranks subtopics by accuracy, worst first. Tags with only a couple of
// attempts are excluded so noise doesn't outrank a genuine weakness.
func (s *Store) weakTags(limit int) ([]TagScore, error) {
	rows, err := s.db.Query(`
		SELECT a.grade, f.tags FROM attempts a JOIN facts f ON f.id = a.fact_id`)
	if err != nil {
		return nil, fmt.Errorf("weak tags: %w", err)
	}
	defer rows.Close()

	tally := map[string]*TagScore{}
	for rows.Next() {
		var grade, tags string
		if err := rows.Scan(&grade, &tags); err != nil {
			return nil, err
		}
		var parsed []string
		json.Unmarshal([]byte(tags), &parsed)
		for _, t := range parsed {
			ts, ok := tally[t]
			if !ok {
				ts = &TagScore{Tag: t}
				tally[t] = ts
			}
			ts.Attempts++
			if model.Grade(grade) == model.GradeCorrect {
				ts.Correct++
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	const minAttempts = 3
	for tag, ts := range tally {
		if ts.Attempts < minAttempts {
			delete(tally, tag)
		}
	}
	return rankWeakest(tally, limit), nil
}

// rankWeakest orders tags by accuracy ascending, breaking ties with the larger
// sample first, and returns at most limit of them.
func rankWeakest(tally map[string]*TagScore, limit int) []TagScore {
	out := make([]TagScore, 0, len(tally))
	for _, ts := range tally {
		if ts.Attempts > 0 {
			ts.Accuracy = float64(ts.Correct) / float64(ts.Attempts)
		}
		out = append(out, *ts)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Accuracy != out[j].Accuracy {
			return out[i].Accuracy < out[j].Accuracy
		}
		if out[i].Attempts != out[j].Attempts {
			return out[i].Attempts > out[j].Attempts
		}
		return out[i].Tag < out[j].Tag
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// dueQueue buckets scheduled reviews by day for the next `days` days.
// Everything already due collapses into a single leading "overdue" bucket.
func (s *Store) dueQueue(now time.Time, days int) ([]DueBucket, error) {
	rows, err := s.db.Query(`SELECT due_at FROM review_state ORDER BY due_at`)
	if err != nil {
		return nil, fmt.Errorf("due queue: %w", err)
	}
	defer rows.Close()

	counts := map[string]int{}
	overdue := 0
	horizon := now.AddDate(0, 0, days)
	for rows.Next() {
		var due string
		if err := rows.Scan(&due); err != nil {
			return nil, err
		}
		d := mustParseTime(due)
		switch {
		case !d.After(now):
			overdue++
		case d.Before(horizon):
			counts[d.Local().Format("2006-01-02")]++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := []DueBucket{{Date: now.Format("2006-01-02"), Count: overdue, Overdue: true}}
	for i := 0; i < days; i++ {
		key := now.AddDate(0, 0, i).Format("2006-01-02")
		out = append(out, DueBucket{Date: key, Count: counts[key]})
	}
	return out, nil
}

// MasteryOf computes current predicted recall for a review state.
func MasteryOf(r model.Review, at time.Time) float64 {
	if r.Reps == 0 {
		return 0
	}
	return fsrs.State{
		Stability: r.Stability, Difficulty: r.Difficulty,
		LastReview: r.LastReview, Reps: r.Reps,
	}.Retrievability(at)
}
