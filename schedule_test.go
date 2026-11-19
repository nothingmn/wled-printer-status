package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nothingmn/wled-printer-status/internal/render"
)

// The tests below are the whole of what proves the time-of-day brightness works, and they keep the
// two rules this repository's suite has: nothing here reads the wall clock, and nothing here opens
// a socket. Every moment is a time.Time the test wrote down, the writer's clock is fakeClock
// (drive_test.go), and the controller is fakeStrip.
//
// The one test here that matters more than the rest is
// TestAScheduleIsJudgedInItsZoneAndNotInTheContainersClock. It is written so that it fails on a
// build that reads the process's own clock, even though that build would pass every other test in
// this file: the process's clock in the test is UTC and the two answers are seven hours apart.

// scheduleOf reads a `brightness_schedule` list the way an operator would paste it, in the zone
// named, and fails the test if the settings are refused.
func scheduleOf(t *testing.T, zone string, lines ...string) *brightnessSchedule {
	t.Helper()

	schedule, err := parseBrightnessSchedule(documents(t, lines...), zone)
	if err != nil {
		t.Fatalf("the schedule %v in %q was refused: %v", lines, zone, err)
	}

	return schedule
}

// documents is the raw JSON a `list` setting carries, one entry per line.
func documents(t *testing.T, lines ...string) []json.RawMessage {
	t.Helper()

	raw := make([]json.RawMessage, 0, len(lines))

	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("the test wrote %q, which is not JSON", line)
		}

		raw = append(raw, json.RawMessage(line))
	}

	return raw
}

// denver is a wall-clock moment in the operator's own zone, written once and read by every test
// that needs one. 18 November 2026 is a Wednesday, and the zone is MST (UTC-7) that week — no
// daylight saving in force, which the daylight-saving assertion in the zone test then varies.
func denver(t *testing.T, hour, minute int) time.Time {
	t.Helper()

	location, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Fatalf("load America/Denver: %v", err)
	}

	return time.Date(2026, time.November, 18, hour, minute, 0, 0, location)
}

// levelNow is the brightness in force at one instant, through the same path the writer uses.
//
// It goes through a writer rather than through the schedule alone on purpose: the fallback to the
// fixed `brightness` is a decision the writer makes, and a test that asked the schedule directly
// would not prove it.
func levelNow(t *testing.T, schedule *brightnessSchedule, at time.Time) int {
	t.Helper()

	pen := newWriter(newFakeStrip(), 128, quiet)
	pen.schedule = schedule

	level, _, _ := pen.levelAt(at)

	return level
}

// TestAWindowOfTheDaySetsTheBrightnessItNames is the plain case: one window in the working day, and
// the light runs at what the window says inside it and at the fixed brightness outside it.
func TestAWindowOfTheDaySetsTheBrightnessItNames(t *testing.T) {
	schedule := scheduleOf(t, "America/Denver",
		`{"from": "09:00", "to": "17:00", "brightness": 200}`)

	cases := []struct {
		at   time.Time
		want int
		why  string
	}{
		{denver(t, 12, 0), 200, "the middle of the window"},
		{denver(t, 8, 59), 128, "a minute before it opens"},
		{denver(t, 17, 1), 128, "a minute after it closes"},
		{denver(t, 3, 0), 128, "the small hours, which no window covers"},
	}

	for _, one := range cases {
		if got := levelNow(t, schedule, one.at); got != one.want {
			t.Errorf("%s (%s): the brightness is %d, want %d",
				one.why, one.at.Format(time.RFC3339), got, one.want)
		}
	}
}

// TestAWindowIncludesItsFirstMinuteAndExcludesItsLast states the rule at the edges, which is the
// one place a schedule is ambiguous and therefore the one place a test has to be explicit:
//
//	from is inside the window, to is outside it. [from, to).
//
// An operator reads "evening until seven" and the light goes back to its fixed brightness on the
// stroke of seven rather than a minute later, and a window written 20:00-07:00 is in force at 20:00
// rather than at 20:01.
func TestAWindowIncludesItsFirstMinuteAndExcludesItsLast(t *testing.T) {
	schedule := scheduleOf(t, "America/Denver",
		`{"from": "09:00", "to": "17:00", "brightness": 200}`)

	cases := []struct {
		hour, minute int
		want         int
		why          string
	}{
		{8, 59, 128, "one minute before from"},
		{9, 0, 200, "exactly from: inside, because a window starts on its own minute"},
		{9, 1, 200, "one minute after from"},
		{16, 59, 200, "one minute before to"},
		{17, 0, 128, "exactly to: outside, because to is the first minute of the light after it"},
	}

	for _, one := range cases {
		if got := levelNow(t, schedule, denver(t, one.hour, one.minute)); got != one.want {
			t.Errorf("at %02d:%02d (%s): the brightness is %d, want %d",
				one.hour, one.minute, one.why, got, one.want)
		}
	}
}

// TestAWindowThatWrapsMidnightCoversBothSidesOfIt is the evening window the operator would write
// first, tested either side of the midnight it runs through.
func TestAWindowThatWrapsMidnightCoversBothSidesOfIt(t *testing.T) {
	schedule := scheduleOf(t, "America/Denver",
		`{"from": "20:00", "to": "07:00", "brightness": 40}`)

	cases := []struct {
		hour, minute int
		want         int
		why          string
	}{
		{19, 59, 128, "a minute before the evening window opens"},
		{20, 0, 40, "exactly the opening minute"},
		{23, 59, 40, "the last minute before midnight"},
		{0, 0, 40, "midnight itself, which is inside a window that wraps"},
		{6, 59, 40, "the last minute of the night"},
		{7, 0, 128, "exactly the closing minute, which is outside it"},
		{12, 0, 128, "the middle of the day"},
	}

	for _, one := range cases {
		if got := levelNow(t, schedule, denver(t, one.hour, one.minute)); got != one.want {
			t.Errorf("at %02d:%02d (%s): the brightness is %d, want %d",
				one.hour, one.minute, one.why, got, one.want)
		}
	}
}

// TestAGapBetweenTwoWindowsFallsBackToTheFixedBrightness is what keeps a schedule safe: a window
// covers part of the day and the rest of the day is the setting this module has always had. A light
// that went dark between two windows would be worse than no schedule at all.
func TestAGapBetweenTwoWindowsFallsBackToTheFixedBrightness(t *testing.T) {
	schedule := scheduleOf(t, "America/Denver",
		`{"from": "07:00", "to": "09:00", "brightness": 220}`,
		`{"from": "19:00", "to": "21:00", "brightness": 60}`)

	cases := []struct {
		hour, minute int
		want         int
		why          string
	}{
		{8, 0, 220, "inside the morning window"},
		{12, 0, 128, "the gap between them, where the fixed brightness stands"},
		{20, 0, 60, "inside the evening window"},
		{22, 0, 128, "after the last window of the day"},
		{2, 0, 128, "before the first one"},
	}

	for _, one := range cases {
		if got := levelNow(t, schedule, denver(t, one.hour, one.minute)); got != one.want {
			t.Errorf("at %02d:%02d (%s): the brightness is %d, want %d",
				one.hour, one.minute, one.why, got, one.want)
		}
	}
}

// TestADayRestrictedWindowAppliesOnlyOnTheDaysItNames pins the other rule a person has to be told
// about, because it is the one that is easy to get wrong when reading a schedule:
//
//	A day names the calendar day the moment falls on, in the zone. A window that wraps midnight with
//	days ["fri"] covers Friday evening to midnight, and the small hours of Saturday belong to
//	Saturday — so a window meant to run all of Friday night names both days.
func TestADayRestrictedWindowAppliesOnlyOnTheDaysItNames(t *testing.T) {
	schedule := scheduleOf(t, "America/Denver",
		`{"from": "22:00", "to": "06:00", "brightness": 30, "days": ["sat", "sun"]}`)

	at := func(day, hour int) time.Time {
		return time.Date(2026, time.November, day, hour, 30, 0, 0, schedule.zone)
	}

	cases := []struct {
		at   time.Time
		want int
		why  string
	}{
		{at(18, 23), 128, "Wednesday night, a day the window does not name"},
		{at(21, 23), 30, "Saturday night, which it does name"},
		{at(19, 2), 128, "two in the morning on Thursday, which the list does not name even though the window that opened on Wednesday night would otherwise have wrapped into it"},
		{at(20, 2), 128, "two in the morning on Friday the 20th, which the list does not name"},
		{at(22, 2), 30, "two in the morning on Sunday the 22nd, which it does"},
	}

	for _, one := range cases {
		if got := levelNow(t, schedule, one.at); got != one.want {
			t.Errorf("%s (%s): the brightness is %d, want %d",
				one.why, one.at.Format("Mon 15:04 MST"), got, one.want)
		}
	}
}

// TestAScheduleIsJudgedInItsZoneAndNotInTheContainersClock is the test this whole change exists to
// make possible, and the one that would have caught the seven-hour bug.
//
// The process runs inside a container whose clock is UTC. The operator is at UTC-7 in November. The
// same instant, judged in a schedule written against each clock, gives two different brightnesses —
// and the container's answer is the wrong one:
//
//	2026-11-18 21:00 UTC is 2026-11-18 14:00 MST: two in the afternoon in Denver. The evening window
//	20:00-07:00 does not cover it there, so the panel runs at the fixed 128. Judged in the
//	container's clock it is 21:00, inside the window, and the panel is dimmed to 40 in the
//	afternoon — the operator's own complaint, arrived at from the module's side.
//
//	2026-11-19 07:30 UTC is 2026-11-19 00:30 MST: half past midnight in Denver, inside the window,
//	so 40. Judged in UTC it is 07:30, past the window's own end, and the light stays bright through
//	the night it was meant to be dim.
//
// Both directions are asserted, because a test that proved only one would pass on a build that had
// the offset backwards or that had written a fixed -7 somewhere. The daylight-saving half of the
// same claim rides along at the end: in July the operator's zone is UTC-6, and a schedule read with
// a hardcoded -7 is an hour out for half the year.
func TestAScheduleIsJudgedInItsZoneAndNotInTheContainersClock(t *testing.T) {
	line := `{"from": "20:00", "to": "07:00", "brightness": 40}`

	inDenver := scheduleOf(t, "America/Denver", line)

	// The same windows, judged in the clock the container actually keeps. Nothing else differs,
	// which is the point: this is the mistake, written out as a schedule.
	inUTC := scheduleOf(t, "UTC", line)

	afternoon := time.Date(2026, time.November, 18, 21, 0, 0, 0, time.UTC)
	midnight := time.Date(2026, time.November, 19, 7, 30, 0, 0, time.UTC)

	cases := []struct {
		at             time.Time
		where          string
		denver, wrongZ int
		why            string
	}{
		{
			at:     afternoon,
			where:  "14:00 in Denver, 21:00 UTC",
			denver: 128,
			wrongZ: 40,
			why:    "two in the afternoon is not the evening, and a schedule read in the container's clock would dim the panel for it",
		},
		{
			at:     midnight,
			where:  "00:30 in Denver, 07:30 UTC",
			denver: 40,
			wrongZ: 128,
			why:    "half past midnight is the evening window's own hours, and a schedule read in the container's clock would have ended it an hour earlier",
		},
	}

	for _, one := range cases {
		if one.denver == one.wrongZ {
			t.Fatalf("%s: both zones answer %d, so this instant cannot tell them apart and the test proves nothing",
				one.where, one.denver)
		}

		if got := levelNow(t, inDenver, one.at); got != one.denver {
			t.Errorf("%s at %s: judged in America/Denver the brightness is %d, want %d (%s)",
				one.at.Format(time.RFC3339), one.where, got, one.denver, one.why)
		}

		if got := levelNow(t, inUTC, one.at); got != one.wrongZ {
			t.Errorf("%s at %s: judged in UTC the brightness is %d, want %d — the same instant in the container's own clock must answer %d here, and it is what this test exists to tell apart from %d",
				one.at.Format(time.RFC3339), one.where, got, one.wrongZ, one.wrongZ, one.denver)
		}
	}

	// Daylight saving. In July Denver is UTC-6, so 02:00 UTC on the 15th is 20:00 on the 14th, the
	// window's own opening minute. Read with a fixed -7 it would be 19:00, an hour before the window
	// opens, and the light would be bright for the first hour of every summer evening.
	summer := time.Date(2026, time.July, 15, 2, 0, 0, 0, time.UTC)

	if got := levelNow(t, inDenver, summer); got != 40 {
		t.Errorf("%s is 20:00 on 14 July in America/Denver and the window's opening minute, so the brightness must be 40; it is %d. Read with a fixed -7 it would be 19:00 and this would be 128",
			summer.Format(time.RFC3339), got)
	}
}

// TestABorrowedSegmentIsNeverGivenABrightnessByTheSchedule is the invariant that predates this
// change, held against the thing that could break it. A borrowed segment of somebody else's
// controller is a zone, and a brightness is controller-wide: SetBrightness moves every segment on
// the box, including the five this module was never pointed at. So a writer that borrows writes no
// brightness at all — not at start, and not when a window's edge arrives under it.
//
// The schedule is set on the writer by hand here, which is the only way to reach the second guard:
// watch.start never hands one to a segment writer, and retune refuses to write for one even if it
// somehow had it. Both guards are the point — the first is the design, the second is the belt.
func TestABorrowedSegmentIsNeverGivenABrightnessByTheSchedule(t *testing.T) {
	strip := borrowedStrip()

	// 19:59 in Denver: a minute before the window opens, so the edge is crossed inside the run
	// rather than before it.
	clock := newFakeClock(denver(t, 19, 59))

	pen := newSegmentWriter(strip, 2, "http://10.0.0.253", quiet)
	pen.schedule = scheduleOf(t, "America/Denver", `{"from": "20:00", "to": "07:00", "brightness": 40}`)
	clock.watch(pen)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	pen.submit(frame(render.ColourIdle, render.ReasonNoLiveJob))
	strip.waitForPaints(t, 1)

	// The window's edge passes under the writer, and a frame that differs from the one on the
	// strip arrives, so the loop turns and paints again rather than suppressing it.
	clock.Advance(2 * time.Minute)

	pen.submit(frame(render.ColourPrinting, render.ReasonProgress))
	strip.waitForPaints(t, 2)

	if settled := strip.settleWrites(); settled != 0 {
		t.Fatalf("the borrowed segment's writer made %d brightness and power writes, want none: a brightness is controller-wide and would move every segment on that controller", settled)
	}

	if borrowed := strip.givenBack(); len(borrowed) != 0 {
		t.Fatalf("the segment was given back while the writer was still running: %+v", borrowed)
	}

	// The paints are the proof that the loop really ran across the boundary: the borrow succeeded,
	// a frame landed, the heartbeat repainted it and the changed frame after the edge went out.
	if len(strip.shown()) < 2 {
		t.Fatalf("the borrowed segment was painted %d times, so the loop did not turn across the window's edge and this test proves nothing about the schedule", len(strip.shown()))
	}
}

// TestAScheduledBrightnessIsWrittenOnceAndOnlyWhenItChanges is the write budget of the schedule: one
// write when the module starts, one at each window's edge, and nothing in between however often the
// light is otherwise talked to. A brightness written on every tick would be a controller knocked on
// sixty times a minute for a value it already has.
func TestAScheduledBrightnessIsWrittenOnceAndOnlyWhenItChanges(t *testing.T) {
	strip := newRecordingStrip()

	// 19:59, one minute before the evening window opens. The fixed brightness is what the module
	// starts at, so the first write is 128 and the second is the window's own 40.
	clock := newFakeClock(denver(t, 19, 59))

	pen := newWriter(strip, 128, quiet)
	pen.schedule = scheduleOf(t, "America/Denver", `{"from": "20:00", "to": "07:00", "brightness": 40}`)
	clock.watch(pen)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go pen.run(ctx)

	// The picture is submitted once and never changes again, which is what an idle printer looks
	// like: the brightness still has to move at the window's edge, on the next turn of the loop.
	still := frame(render.ColourIdle, render.ReasonNoLiveJob)

	pen.submit(still)
	strip.waitForPaints(t, 1)

	strip.waitForLevels(t, []int{128})

	// The window opens. Nothing about the printer changed; the clock did.
	clock.Advance(2 * time.Minute)
	pen.submit(still)

	strip.waitForLevels(t, []int{128, 40})

	// Ten minutes of the same window, with the same frame arriving the way the driver's tick
	// delivers it. Not one more brightness write.
	for range 20 {
		clock.Advance(30 * time.Second)

		pen.submit(still)
	}

	painted := len(strip.shown())

	if got := strip.levels(); len(got) != 2 {
		t.Fatalf("the light's brightness was written %d times (%v) across ten minutes of one window, want 2: the value it started with and the window's value", len(got), got)
	}

	// The window's end, which puts the fixed brightness back on the stroke of seven. The wait the
	// loop is in is crossed by the clock, and a frame follows so the loop certainly turns.
	clock.Advance(11 * time.Hour)
	pen.submit(still)

	strip.waitForLevels(t, []int{128, 40, 128})

	if shown := strip.shown(); len(shown) == painted {
		t.Log("the loop painted no new frame across eleven hours, which is right: the brightness moved and the picture did not")
	}
}

// TestAScheduleWithNoZoneIsRefusedAtStartAndTheMessageNamesBoth is the refusal that keeps a
// seven-hour mistake out of an install: a schedule is judged in a zone the operator names, and a
// start without one stops with a sentence rather than running on the container's clock.
//
// The message has to name both settings — the one that is wrong and the one that is missing —
// because it is read on an instance by a person who has just pasted two fields and cannot see which
// of them the module is complaining about.
func TestAScheduleWithNoZoneIsRefusedAtStartAndTheMessageNamesBoth(t *testing.T) {
	schedule := documents(t, `{"from": "20:00", "to": "07:00", "brightness": 40}`)

	err := Settings{
		ControllerAddress:  "http://10.0.0.136",
		Brightness:         128,
		BrightnessSchedule: schedule,
	}.validate()

	if err == nil {
		t.Fatal("a schedule with no timezone was accepted, and it would be judged in the container's own clock, seven hours from the operator's evening")
	}

	for _, want := range []string{"brightness_schedule", "timezone", "America/Denver", "UTC"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal reads %q, and it must name %q", err, want)
		}
	}
}

// TestAZoneThatWillNotLoadIsRefusedWithItsOwnName is the other half: a zone is not accepted on the
// strength of being written down. It is refused with the value quoted, so that a misspelling is a
// sentence on the instance rather than a light an hour or seven hours out.
//
// It is refused even while no schedule is written, which is deliberate: a zone accepted today is a
// start refused tomorrow, on the save that adds the first window, and by then the operator has been
// told his zone was fine.
func TestAZoneThatWillNotLoadIsRefusedWithItsOwnName(t *testing.T) {
	schedule := documents(t, `{"from": "20:00", "to": "07:00", "brightness": 40}`)

	err := Settings{
		ControllerAddress:  "http://10.0.0.136",
		Brightness:         128,
		BrightnessSchedule: schedule,
		Timezone:           "America/Denvr",
	}.validate()

	if err == nil {
		t.Fatal("the zone America/Denvr was accepted, and a schedule judged in a zone that does not exist would be judged in something nobody chose")
	}

	for _, want := range []string{"timezone", "America/Denvr", "America/Denver"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal reads %q, and it must name %q", err, want)
		}
	}

	if err := (Settings{Timezone: "Mars/Olympus"}).validate(); err == nil {
		t.Fatal("a zone that cannot load was accepted with no schedule written, so the typo would only surface on the save that adds one")
	}

	// With no schedule at all an empty zone costs nothing, which is what lets every install that
	// predates this setting keep starting unchanged.
	if err := (Settings{ControllerAddress: "http://10.0.0.136", Brightness: 128}).validate(); err != nil {
		t.Fatalf("an install with no schedule and no zone was refused: %v", err)
	}

	// And the good pair is accepted, or nothing above means anything.
	if err := (Settings{
		ControllerAddress:  "http://10.0.0.136",
		Brightness:         128,
		BrightnessSchedule: schedule,
		Timezone:           "America/Denver",
	}).validate(); err != nil {
		t.Fatalf("a schedule in America/Denver was refused: %v", err)
	}
}

// TestAMalformedWindowIsRefusedWithItsLineAndField holds every refusal a pasted list can earn to the
// line and the field, because the person reading it is looking at a text area and counting lines.
func TestAMalformedWindowIsRefusedWithItsLineAndField(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  []string
	}{
		{
			name:  "a misspelt key",
			lines: []string{`{"from": "09:00", "to": "17:00", "brightness": 200}`, `{"from": "20:00", "to": "07:00", "brigthness": 40}`},
			want:  []string{"brightness_schedule[1]", "brigthness"},
		},
		{
			name:  "a time that is not a time",
			lines: []string{`{"from": "25:00", "to": "07:00", "brightness": 40}`},
			want:  []string{"brightness_schedule[0]", "from", "25:00"},
		},
		{
			name:  "an end that is not a time",
			lines: []string{`{"from": "20:00", "to": "seven", "brightness": 40}`},
			want:  []string{"brightness_schedule[0]", "to", "seven"},
		},
		{
			name:  "no brightness at all",
			lines: []string{`{"from": "20:00", "to": "07:00"}`},
			want:  []string{"brightness_schedule[0]", "no brightness", "20:00-07:00"},
		},
		{
			name:  "a brightness no controller takes",
			lines: []string{`{"from": "20:00", "to": "07:00", "brightness": 300}`},
			want:  []string{"brightness_schedule[0]", "300", "0-255"},
		},
		{
			name:  "a window with no time in it",
			lines: []string{`{"from": "22:00", "to": "22:00", "brightness": 40}`},
			want:  []string{"brightness_schedule[0]", "22:00", "no time at all"},
		},
		{
			name:  "a day that is not a day",
			lines: []string{`{"from": "20:00", "to": "07:00", "brightness": 40, "days": ["funday"]}`},
			want:  []string{"brightness_schedule[0]", "funday", "mon, tue, wed, thu, fri, sat or sun"},
		},
		{
			name:  "the second line is the broken one",
			lines: []string{`{"from": "07:00", "to": "09:00", "brightness": 220}`, `{"from": "19:00", "brightness": 60}`},
			want:  []string{"brightness_schedule[1]", "to is empty"},
		},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			_, err := parseBrightnessSchedule(documents(t, one.lines...), "America/Denver")
			if err == nil {
				t.Fatalf("%v was accepted", one.lines)
			}

			for _, want := range one.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal reads %q, and it must name %q", err, want)
				}
			}
		})
	}
}

// TestAScheduleWithNoWindowsLeavesTheFixedBrightnessAlone is the promise that this change costs an
// existing install nothing: no schedule, no zone, no write, and the brightness the operator set is
// the whole answer — the behaviour this module had before the setting existed.
func TestAScheduleWithNoWindowsLeavesTheFixedBrightnessAlone(t *testing.T) {
	schedule, err := parseBrightnessSchedule(nil, "")
	if err != nil || schedule != nil {
		t.Fatalf("no list and no zone produced the schedule %v and the error %v, want neither", schedule, err)
	}

	// The same answer when the operator clears the list rather than never writing it: an empty
	// array decodes to no windows at all.
	empty, err := parseBrightnessSchedule(documents(t), "")
	if err != nil || empty != nil {
		t.Fatalf("an empty brightness_schedule produced the schedule %v and the error %v, want neither", empty, err)
	}

	pen := newWriter(newFakeStrip(), 96, quiet)

	if pen.schedule != nil {
		t.Fatal("a writer built with no schedule carries one")
	}

	level, why, covered := pen.levelAt(denver(t, 20, 30))
	if level != 96 || covered || why != "" {
		t.Fatalf("with no schedule the brightness at 20:30 is %d (%q, covered=%t), want the fixed 96 and nothing to explain",
			level, why, covered)
	}
}

// recordingStrip is fakeStrip with the brightness values kept, in order, so that a test can say what
// the light was asked to run at rather than only how many times it was asked.
type recordingStrip struct {
	*fakeStrip

	mu   sync.Mutex
	sent []int
}

// newRecordingStrip is a controller that remembers every brightness it was given.
func newRecordingStrip() *recordingStrip {
	return &recordingStrip{fakeStrip: newFakeStrip()}
}

// SetBrightness records the value and then behaves exactly like the fake underneath, which keeps
// the started-at count the rest of the suite asserts on.
func (r *recordingStrip) SetBrightness(ctx context.Context, level int) error {
	r.mu.Lock()
	r.sent = append(r.sent, level)
	r.mu.Unlock()

	return r.fakeStrip.SetBrightness(ctx, level)
}

// levels is every brightness the controller was given, in order.
func (r *recordingStrip) levels() []int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]int(nil), r.sent...)
}

// waitForLevels waits until the controller has been given exactly these brightness values, in this
// order, and fails the test if it never is.
func (r *recordingStrip) waitForLevels(t *testing.T, want []int) {
	t.Helper()

	deadline := time.Now().Add(howLong)

	for {
		got := r.levels()

		if len(got) == len(want) {
			same := true

			for i := range want {
				if got[i] != want[i] {
					same = false
				}
			}

			if same {
				return
			}
		}

		if time.Now().After(deadline) {
			t.Fatalf("the light's brightness was written %v, want %v", got, want)
		}

		time.Sleep(time.Millisecond)
	}
}
