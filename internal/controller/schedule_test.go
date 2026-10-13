package controller

import (
	"testing"
	"time"

	kirocrewv1alpha1 "github.com/lsdopen/kiro-crew-operator/api/v1alpha1"
)

const (
	weekdayUp    = "0 8 * * 1-5"
	weekdayDown  = "0 18 * * 1-5"
	tzUTC        = "UTC"
	johannesburg = "Africa/Johannesburg"
	eveningDown  = "2026-10-12T18:00:00Z"
)

func specWith(tz string) kirocrewv1alpha1.KiroCrewSpec {
	return kirocrewv1alpha1.KiroCrewSpec{
		Owner:    testOwner,
		Schedule: &kirocrewv1alpha1.ScheduleSpec{Up: weekdayUp, Down: weekdayDown, Timezone: tz},
	}
}

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestComputeDesiredWindow(t *testing.T) {
	// 2026-10-12 is a Monday.
	tests := []struct {
		name     string
		tz       string
		now      string
		replicas int32
		reason   string
		next     string
	}{
		{"inside window", tzUTC, "2026-10-12T10:00:00Z", 1, "", eveningDown},
		{"before up on a weekday", tzUTC, "2026-10-12T07:00:00Z", 0, kirocrewv1alpha1.ReasonOutsideSchedule, "2026-10-12T08:00:00Z"},
		{"after down", tzUTC, "2026-10-12T19:00:00Z", 0, kirocrewv1alpha1.ReasonOutsideSchedule, "2026-10-13T08:00:00Z"},
		{"exactly at up", tzUTC, "2026-10-12T08:00:00Z", 1, "", eveningDown},
		{"exactly at down", tzUTC, eveningDown, 0, kirocrewv1alpha1.ReasonOutsideSchedule, "2026-10-13T08:00:00Z"},
		{"saturday is off", tzUTC, "2026-10-17T10:00:00Z", 0, kirocrewv1alpha1.ReasonOutsideSchedule, "2026-10-19T08:00:00Z"},
		{"sunday is off", tzUTC, "2026-10-18T23:00:00Z", 0, kirocrewv1alpha1.ReasonOutsideSchedule, "2026-10-19T08:00:00Z"},
		// Johannesburg is UTC+2: 07:00Z is 09:00 local, inside the window.
		{"timezone offset inside", johannesburg, "2026-10-12T07:00:00Z", 1, "", "2026-10-12T16:00:00Z"},
		// 06:00Z is 08:00 local: exactly up. 05:59Z is still before.
		{"timezone offset boundary", johannesburg, "2026-10-12T05:59:00Z", 0, kirocrewv1alpha1.ReasonOutsideSchedule, "2026-10-12T06:00:00Z"},
		{"timezone offset after down", johannesburg, "2026-10-12T16:00:00Z", 0, kirocrewv1alpha1.ReasonOutsideSchedule, "2026-10-13T06:00:00Z"},
		{"empty timezone is UTC", "", "2026-10-12T10:00:00Z", 1, "", eveningDown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := specWith(tt.tz)
			got := computeDesired(&spec, utc(tt.now))
			if got.ScheduleErr != nil {
				t.Fatalf("unexpected error: %v", got.ScheduleErr)
			}
			if got.Replicas != tt.replicas || got.Reason != tt.reason {
				t.Errorf("got replicas=%d reason=%q, want %d %q", got.Replicas, got.Reason, tt.replicas, tt.reason)
			}
			if got.Next == nil || !got.Next.Equal(utc(tt.next)) {
				t.Errorf("next = %v, want %s", got.Next, tt.next)
			}
		})
	}
}

func TestComputeDesiredNoScheduleRuns(t *testing.T) {
	spec := kirocrewv1alpha1.KiroCrewSpec{Owner: testOwner}
	got := computeDesired(&spec, utc("2026-10-12T10:00:00Z"))
	if got.Replicas != 1 || got.Reason != "" || got.Next != nil {
		t.Errorf("got %+v, want plain running", got)
	}
}

func TestComputeDesiredSuspendOverridesSchedule(t *testing.T) {
	spec := specWith(tzUTC)
	spec.Suspend = true
	got := computeDesired(&spec, utc("2026-10-12T10:00:00Z")) // inside the window
	if got.Replicas != 0 || got.Reason != kirocrewv1alpha1.ReasonSuspended {
		t.Errorf("got replicas=%d reason=%q, want 0 Suspended", got.Replicas, got.Reason)
	}
	if got.Next != nil {
		t.Errorf("suspended crew should have no next transition, got %v", got.Next)
	}
}

func TestComputeDesiredSuspendIgnoresInvalidSchedule(t *testing.T) {
	spec := specWith(tzUTC)
	spec.Suspend = true
	spec.Schedule.Up = "garbage"
	if got := computeDesired(&spec, utc("2026-10-12T10:00:00Z")); got.Replicas != 0 || got.ScheduleErr != nil {
		t.Errorf("got %+v, want suspended without error", got)
	}
}

func TestComputeDesiredInvalidScheduleFailsOpenWithError(t *testing.T) {
	for name, mutate := range map[string]func(*kirocrewv1alpha1.ScheduleSpec){
		"bad up":       func(s *kirocrewv1alpha1.ScheduleSpec) { s.Up = "not a cron" },
		"bad down":     func(s *kirocrewv1alpha1.ScheduleSpec) { s.Down = "99 99 * * *" },
		"six fields":   func(s *kirocrewv1alpha1.ScheduleSpec) { s.Up = "0 0 8 * * 1-5" },
		"bad timezone": func(s *kirocrewv1alpha1.ScheduleSpec) { s.Timezone = "Mars/Olympus" },
	} {
		t.Run(name, func(t *testing.T) {
			spec := specWith(tzUTC)
			mutate(spec.Schedule)
			got := computeDesired(&spec, utc("2026-10-12T22:00:00Z"))
			if got.ScheduleErr == nil {
				t.Fatal("expected an error")
			}
			if got.Replicas != 1 || got.Next != nil {
				t.Errorf("got %+v, want running with no next transition", got)
			}
		})
	}
}

func TestComputeDesiredSameInstantDownWins(t *testing.T) {
	spec := specWith(tzUTC)
	spec.Schedule.Up, spec.Schedule.Down = "0 8 * * *", "0 8 * * *"
	if got := computeDesired(&spec, utc("2026-10-12T08:00:00Z")); got.Replicas != 0 {
		t.Errorf("replicas = %d, want 0 on a tie", got.Replicas)
	}
}

func TestComputeDesiredWindowCrossingMidnight(t *testing.T) {
	spec := specWith(tzUTC)
	spec.Schedule.Up, spec.Schedule.Down = "0 22 * * *", "0 6 * * *"
	if got := computeDesired(&spec, utc("2026-10-13T02:00:00Z")); got.Replicas != 1 {
		t.Errorf("02:00 replicas = %d, want 1", got.Replicas)
	}
	if got := computeDesired(&spec, utc("2026-10-13T12:00:00Z")); got.Replicas != 0 {
		t.Errorf("12:00 replicas = %d, want 0", got.Replicas)
	}
}

func TestReconcilerNowDefaultsToWallClock(t *testing.T) {
	r := &KiroCrewReconciler{}
	if d := time.Since(r.now()); d < 0 || d > time.Minute {
		t.Errorf("default clock off by %v", d)
	}
	fixed := utc("2026-10-12T10:00:00Z")
	r.Now = func() time.Time { return fixed }
	if !r.now().Equal(fixed) {
		t.Error("injected clock ignored")
	}
}
