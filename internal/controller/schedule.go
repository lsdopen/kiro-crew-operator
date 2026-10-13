package controller

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"

	kirocrewv1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
)

// maxLookback bounds the search for the most recent fire of a cron expression.
const maxLookback = 2 * 366 * 24 * time.Hour

// desiredState is what the schedule and suspend flag resolve to at a point in time.
type desiredState struct {
	Replicas int32
	// Reason is empty when the crew should run unscheduled.
	Reason string
	// Next is when the schedule next flips the state; nil without a valid schedule.
	Next *time.Time
	// ScheduleErr is set when spec.schedule is unusable; the crew then runs.
	ScheduleErr error
}

// computeDesired resolves replicas for the crew at now. Suspend wins over
// schedule; spec.startedAt counts as an Up event on a scheduled crew. An invalid
// schedule fails open (runs) and is reported via ScheduleErr.
func computeDesired(spec *kirocrewv1alpha1.KiroCrewSpec, now time.Time) desiredState {
	if spec.Suspend {
		return desiredState{Replicas: 0, Reason: kirocrewv1alpha1.ReasonSuspended}
	}
	if spec.Schedule == nil {
		return desiredState{Replicas: 1}
	}
	var started time.Time
	if spec.StartedAt != nil {
		started = spec.StartedAt.Time
	}
	on, next, err := inUpWindow(spec.Schedule, started, now)
	if err != nil {
		return desiredState{Replicas: 1, ScheduleErr: err}
	}
	if on {
		return desiredState{Replicas: 1, Next: &next}
	}
	return desiredState{Replicas: 0, Reason: kirocrewv1alpha1.ReasonOutsideSchedule, Next: &next}
}

// inUpWindow reports whether the crew is up at now and when the state next flips.
// started is an extra Up event (zero for none); the latest event wins, and a Down
// wins a tie.
func inUpWindow(s *kirocrewv1alpha1.ScheduleSpec, started, now time.Time) (bool, time.Time, error) {
	tz := s.Timezone
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return false, time.Time{}, fmt.Errorf("invalid timezone %q: %w", tz, err)
	}
	up, err := cron.ParseStandard(s.Up)
	if err != nil {
		return false, time.Time{}, fmt.Errorf("invalid schedule.up %q: %w", s.Up, err)
	}
	down, err := cron.ParseStandard(s.Down)
	if err != nil {
		return false, time.Time{}, fmt.Errorf("invalid schedule.down %q: %w", s.Down, err)
	}

	now = now.In(loc)
	nextUp, nextDown := up.Next(now), down.Next(now)
	if nextUp.IsZero() || nextDown.IsZero() {
		return false, time.Time{}, fmt.Errorf("schedule never fires")
	}
	lastUp, lastDown := lastFire(up, now), lastFire(down, now)
	// A startedAt ahead of this clock (skew) counts as now, or Start would stall.
	if started.After(now) {
		started = now
	}
	if started.After(lastUp) {
		lastUp = started
	}

	var on bool
	switch {
	case lastUp.IsZero() && lastDown.IsZero():
		// No past fire to go on: the crew is up if the next flip is a down.
		on = nextDown.Before(nextUp)
	default:
		on = lastUp.After(lastDown)
	}
	if on {
		return true, nextDown, nil
	}
	return false, nextUp, nil
}

// lastFire returns the latest fire time at or before now, or the zero time if
// none falls within maxLookback.
func lastFire(s cron.Schedule, now time.Time) time.Time {
	for window := time.Hour; ; window *= 8 {
		if window > maxLookback {
			window = maxLookback
		}
		var last time.Time
		for t := s.Next(now.Add(-window - time.Second)); !t.IsZero() && !t.After(now); t = s.Next(t) {
			last = t
		}
		if !last.IsZero() || window == maxLookback {
			return last
		}
	}
}
