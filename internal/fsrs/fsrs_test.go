package fsrs

import (
	"math"
	"testing"
	"time"
)

var epoch = time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)

// At the default 0.9 request retention the scheduling interval must equal
// stability exactly — that identity is what fixes the `factor` constant.
func TestIntervalEqualsStabilityAtDefaultRetention(t *testing.T) {
	sc := NewScheduler()
	for _, s := range []float64{2, 10, 137.5} {
		if got := sc.IntervalDays(s); math.Abs(got-s) > 1e-9 {
			t.Errorf("IntervalDays(%v) = %v, want %v", s, got, s)
		}
	}
}

func TestRetrievabilityDecays(t *testing.T) {
	s := State{Stability: 10, Difficulty: 5, LastReview: epoch, Reps: 1}

	if r := s.Retrievability(epoch); math.Abs(r-1) > 1e-9 {
		t.Errorf("retrievability at review time = %v, want 1", r)
	}
	// By definition, retrievability is 0.9 one stability-length later.
	if r := s.Retrievability(epoch.AddDate(0, 0, 10)); math.Abs(r-0.9) > 1e-6 {
		t.Errorf("retrievability after S days = %v, want 0.9", r)
	}
	prev := 1.0
	for d := 1; d <= 60; d++ {
		r := s.Retrievability(epoch.AddDate(0, 0, d))
		if r >= prev {
			t.Fatalf("retrievability not monotonically decreasing at day %d: %v >= %v", d, r, prev)
		}
		prev = r
	}
	if (State{}).Retrievability(epoch) != 0 {
		t.Error("a new state should have zero retrievability")
	}
}

func TestInitialReviewOrdersStabilityByRating(t *testing.T) {
	sc := NewScheduler()
	var last float64
	for _, r := range []Rating{Again, Hard, Good, Easy} {
		s := sc.Review(State{}, r, epoch)
		if s.Reps != 1 {
			t.Errorf("rating %v: Reps = %d, want 1", r, s.Reps)
		}
		if s.Stability <= last {
			t.Errorf("rating %v: stability %v not greater than previous %v", r, s.Stability, last)
		}
		if s.Difficulty < minDifficulty || s.Difficulty > maxDifficulty {
			t.Errorf("rating %v: difficulty %v out of range", r, s.Difficulty)
		}
		last = s.Stability
	}
}

func TestDifficultyMovesWithGrade(t *testing.T) {
	sc := NewScheduler()
	base := State{Stability: 10, Difficulty: 5, LastReview: epoch, Reps: 3}
	later := epoch.AddDate(0, 0, 10)

	if got := sc.Review(base, Again, later).Difficulty; got <= base.Difficulty {
		t.Errorf("Again should raise difficulty: %v -> %v", base.Difficulty, got)
	}
	if got := sc.Review(base, Easy, later).Difficulty; got >= base.Difficulty {
		t.Errorf("Easy should lower difficulty: %v -> %v", base.Difficulty, got)
	}
}

func TestSuccessGrowsStabilityAndLapseShrinksIt(t *testing.T) {
	sc := NewScheduler()
	base := State{Stability: 10, Difficulty: 5, LastReview: epoch, Reps: 3}
	later := epoch.AddDate(0, 0, 10)

	good := sc.Review(base, Good, later)
	if good.Stability <= base.Stability {
		t.Errorf("Good should grow stability: %v -> %v", base.Stability, good.Stability)
	}
	if good.Lapses != base.Lapses {
		t.Errorf("Good should not record a lapse")
	}

	again := sc.Review(base, Again, later)
	if again.Stability > base.Stability {
		t.Errorf("a lapse must never increase stability: %v -> %v", base.Stability, again.Stability)
	}
	if again.Lapses != base.Lapses+1 {
		t.Errorf("Lapses = %d, want %d", again.Lapses, base.Lapses+1)
	}

	easy := sc.Review(base, Easy, later)
	hard := sc.Review(base, Hard, later)
	if !(hard.Stability < good.Stability && good.Stability < easy.Stability) {
		t.Errorf("stability gain should order Hard < Good < Easy, got %v, %v, %v",
			hard.Stability, good.Stability, easy.Stability)
	}
}

// The spacing effect: reviewing later, when retrievability has decayed further,
// should yield a larger stability gain than reviewing early.
func TestSpacingEffect(t *testing.T) {
	sc := NewScheduler()
	base := State{Stability: 10, Difficulty: 5, LastReview: epoch, Reps: 3}

	early := sc.Review(base, Good, epoch.AddDate(0, 0, 3))
	late := sc.Review(base, Good, epoch.AddDate(0, 0, 20))
	if late.Stability <= early.Stability {
		t.Errorf("delayed review should gain more stability: early %v, late %v",
			early.Stability, late.Stability)
	}
}

// A missed fact must come back inside the same sitting, not tomorrow.
func TestAgainReschedulesWithinSession(t *testing.T) {
	sc := NewScheduler()
	s := sc.Review(State{Stability: 30, Difficulty: 5, LastReview: epoch, Reps: 5}, Again, epoch.AddDate(0, 0, 30))
	wait := s.Due.Sub(epoch.AddDate(0, 0, 30))
	if wait != againStep {
		t.Errorf("Again due in %v, want %v", wait, againStep)
	}
}

func TestGoodSchedulesDaysAhead(t *testing.T) {
	sc := NewScheduler()
	now := epoch.AddDate(0, 0, 10)
	s := sc.Review(State{Stability: 10, Difficulty: 5, LastReview: epoch, Reps: 3}, Good, now)
	if !s.Due.After(now.AddDate(0, 0, 1)) {
		t.Errorf("Good on a stable fact should schedule more than a day out, got %v", s.Due.Sub(now))
	}
}

func TestSameDayReviewUsesShortTermUpdate(t *testing.T) {
	sc := NewScheduler()
	base := State{Stability: 10, Difficulty: 5, LastReview: epoch, Reps: 2}
	// Two hours later: elapsed < 1 day.
	s := sc.Review(base, Good, epoch.Add(2*time.Hour))
	if s.Stability <= base.Stability {
		t.Errorf("same-day Good should nudge stability up: %v -> %v", base.Stability, s.Stability)
	}
	// The short-term update must be far smaller than a properly spaced one.
	spaced := sc.Review(base, Good, epoch.AddDate(0, 0, 10))
	if s.Stability >= spaced.Stability {
		t.Errorf("cramming (%v) should not beat spacing (%v)", s.Stability, spaced.Stability)
	}
}

func TestStateStaysInBounds(t *testing.T) {
	sc := NewScheduler()
	s := State{}
	now := epoch
	// Hammer the scheduler with an adversarial grade sequence.
	for i := 0; i < 500; i++ {
		r := Rating(1 + i%4)
		s = sc.Review(s, r, now)
		if s.Stability < minStability || s.Stability > maxStability || math.IsNaN(s.Stability) {
			t.Fatalf("iteration %d: stability out of bounds: %v", i, s.Stability)
		}
		if s.Difficulty < minDifficulty || s.Difficulty > maxDifficulty || math.IsNaN(s.Difficulty) {
			t.Fatalf("iteration %d: difficulty out of bounds: %v", i, s.Difficulty)
		}
		if !s.Due.After(now) {
			t.Fatalf("iteration %d: due date %v is not after now %v", i, s.Due, now)
		}
		now = s.Due
	}
}

func TestInvalidRatingFallsBackToGood(t *testing.T) {
	sc := NewScheduler()
	if got, want := sc.Review(State{}, Rating(99), epoch), sc.Review(State{}, Good, epoch); got.Stability != want.Stability {
		t.Errorf("invalid rating should behave as Good, got %v want %v", got.Stability, want.Stability)
	}
}
