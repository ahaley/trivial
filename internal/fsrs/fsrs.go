// Package fsrs implements the Free Spaced Repetition Scheduler (FSRS-5), the
// scheduling algorithm behind Trivial's pedagogy engine (SPEC.md §7).
//
// FSRS models each fact with two latent variables:
//
//	Stability  S — days until recall probability decays to 90%.
//	Difficulty D — intrinsic hardness of the fact, on [1, 10].
//
// After each review both are updated from the grade and from how much
// retrievability had decayed at review time, and the next due date is the point
// where predicted retrievability falls to the requested retention.
package fsrs

import (
	"math"
	"time"
)

// Rating is the four-point grade FSRS schedules against.
type Rating int

const (
	Again Rating = 1 // failed to recall
	Hard  Rating = 2 // recalled with serious difficulty
	Good  Rating = 3 // recalled correctly
	Easy  Rating = 4 // recalled instantly and confidently
)

// Valid reports whether r is one of the four defined ratings.
func (r Rating) Valid() bool { return r >= Again && r <= Easy }

// Forgetting-curve constants. R(t) = (1 + factor*t/S)^decay is FSRS-5's
// power-law curve; factor is fixed so that R(S) = 0.9 exactly.
const (
	decay  = -0.5
	factor = 19.0 / 81.0

	minStability  = 0.01
	maxStability  = 36500.0 // 100 years
	minDifficulty = 1.0
	maxDifficulty = 10.0
)

// DefaultWeights is the FSRS-5 default parameter set, fitted by the FSRS
// project against a large public review corpus. Trivial ships these as-is;
// per-user optimisation from local history is possible but not implemented.
var DefaultWeights = Weights{
	0.40255, 1.18385, 3.173, 15.69105, 7.1949, 0.5345, 1.4604, 0.0046,
	1.54575, 0.1192, 1.01925, 1.9395, 0.11, 0.29605, 2.2698, 0.2315,
	2.9898, 0.51655, 0.6621,
}

// Weights are the 19 FSRS-5 model parameters.
type Weights [19]float64

// Sub-day relearning steps. FSRS schedules on a scale of days, which alone
// would mean a fact missed today cannot return until tomorrow. Trivial layers
// the conventional short relearning steps on top so a miss is re-tested inside
// the same sitting — the "repeat lacking areas now" half of SPEC.md §7.
const (
	againStep = 5 * time.Minute
	hardStep  = 10 * time.Minute
)

// Scheduler schedules reviews for a given weight set and target retention.
type Scheduler struct {
	// W is the FSRS parameter set.
	W Weights
	// RequestRetention is the recall probability to schedule for, on (0, 1).
	// 0.9 is the FSRS default and keeps workload and retention balanced.
	RequestRetention float64
	// MaximumInterval caps how far ahead a review may be pushed, in days.
	MaximumInterval float64
}

// NewScheduler returns a Scheduler with FSRS-5 defaults.
func NewScheduler() *Scheduler {
	return &Scheduler{
		W:                DefaultWeights,
		RequestRetention: 0.9,
		MaximumInterval:  365 * 10,
	}
}

// State is a fact's scheduling state. The zero State means "never reviewed".
type State struct {
	Stability  float64
	Difficulty float64
	Due        time.Time
	LastReview time.Time
	Reps       int
	Lapses     int
}

// New reports whether the fact has never been reviewed.
func (s State) New() bool { return s.Reps == 0 || s.LastReview.IsZero() }

// Retrievability is the predicted probability of recalling the fact at time t.
// A never-reviewed fact returns 0.
func (s State) Retrievability(t time.Time) float64 {
	if s.New() || s.Stability <= 0 {
		return 0
	}
	elapsed := math.Max(0, t.Sub(s.LastReview).Hours()/24)
	return math.Pow(1+factor*elapsed/s.Stability, decay)
}

// Review applies a grade at time now and returns the updated state.
func (sc *Scheduler) Review(s State, rating Rating, now time.Time) State {
	if !rating.Valid() {
		rating = Good
	}

	next := State{Reps: s.Reps + 1, Lapses: s.Lapses}

	switch {
	case s.New():
		next.Stability = sc.initialStability(rating)
		next.Difficulty = sc.initialDifficulty(rating)
		if rating == Again {
			next.Lapses++
		}

	default:
		elapsed := math.Max(0, now.Sub(s.LastReview).Hours()/24)
		r := math.Pow(1+factor*elapsed/math.Max(s.Stability, minStability), decay)
		next.Difficulty = sc.nextDifficulty(s.Difficulty, rating)

		switch {
		case elapsed < 1:
			// Same-day review: memory has barely decayed, so FSRS-5 uses a
			// dedicated short-term update rather than the full curve.
			next.Stability = s.Stability * math.Exp(sc.W[17]*(float64(rating)-3+sc.W[18]))
			if rating == Again {
				next.Lapses++
			}
		case rating == Again:
			next.Stability = sc.postLapseStability(s.Stability, next.Difficulty, r)
			next.Lapses++
		default:
			next.Stability = sc.postRecallStability(s.Stability, next.Difficulty, r, rating)
		}
	}

	next.Stability = clamp(next.Stability, minStability, maxStability)
	next.Difficulty = clamp(next.Difficulty, minDifficulty, maxDifficulty)
	next.LastReview = now
	next.Due = sc.due(next, rating, now)
	return next
}

// due converts stability into a wall-clock due time, applying the short
// relearning steps for grades that signal the fact is not yet secure.
func (sc *Scheduler) due(s State, rating Rating, now time.Time) time.Time {
	switch {
	case rating == Again:
		return now.Add(againStep)
	case rating == Hard && s.Stability < 1:
		return now.Add(hardStep)
	}
	return now.Add(time.Duration(sc.IntervalDays(s.Stability) * float64(24*time.Hour)))
}

// IntervalDays is the interval, in days, at which retrievability falls to the
// requested retention. At the default 0.9 retention this equals stability.
func (sc *Scheduler) IntervalDays(stability float64) float64 {
	iv := stability / factor * (math.Pow(sc.RequestRetention, 1/decay) - 1)
	return clamp(iv, 1, sc.MaximumInterval)
}

func (sc *Scheduler) initialStability(rating Rating) float64 {
	return clamp(sc.W[rating-1], minStability, maxStability)
}

func (sc *Scheduler) initialDifficulty(rating Rating) float64 {
	d := sc.W[4] - math.Exp(sc.W[5]*float64(rating-1)) + 1
	return clamp(d, minDifficulty, maxDifficulty)
}

// nextDifficulty applies FSRS-5's linearly damped update plus mean reversion
// toward the difficulty an "Easy" first answer would have produced.
func (sc *Scheduler) nextDifficulty(d float64, rating Rating) float64 {
	delta := -sc.W[6] * float64(rating-3)
	damped := d + delta*(10-d)/9
	reverted := sc.W[7]*sc.initialDifficulty(Easy) + (1-sc.W[7])*damped
	return clamp(reverted, minDifficulty, maxDifficulty)
}

// postRecallStability grows stability after a successful recall. The gain is
// largest for easy facts recalled when retrievability had already dropped —
// the spacing effect.
func (sc *Scheduler) postRecallStability(s, d, r float64, rating Rating) float64 {
	hardPenalty := 1.0
	if rating == Hard {
		hardPenalty = sc.W[15]
	}
	easyBonus := 1.0
	if rating == Easy {
		easyBonus = sc.W[16]
	}
	gain := math.Exp(sc.W[8]) *
		(11 - d) *
		math.Pow(s, -sc.W[9]) *
		(math.Exp(sc.W[10]*(1-r)) - 1) *
		hardPenalty * easyBonus
	return s * (1 + gain)
}

// postLapseStability collapses stability after a failure. FSRS-5 caps the
// result at the previous stability so a lapse can never be a promotion.
func (sc *Scheduler) postLapseStability(s, d, r float64) float64 {
	sf := sc.W[11] *
		math.Pow(d, -sc.W[12]) *
		(math.Pow(s+1, sc.W[13]) - 1) *
		math.Exp(sc.W[14]*(1-r))
	return math.Min(sf, s)
}

func clamp(v, lo, hi float64) float64 {
	if math.IsNaN(v) {
		return lo
	}
	return math.Max(lo, math.Min(hi, v))
}
