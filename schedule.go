package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The brightness the operator asked for, in the two halves he named: a fixed value, and a
// value that follows the time of day.
//
// The fixed half is the `brightness` setting and has always been here. This file is the second
// half — a list of windows an operator pastes into the settings pane, each one a span of the
// day and the brightness to run at inside it — and the one decision that makes it worth a file
// of its own: **a window is judged in a zone the operator names, never in the process's own
// clock.**
//
// That is not fussiness. The process runs inside the Prusa Connect container, whose clock is
// almost always UTC, and the operator lives at UTC-7 in a zone that also observes daylight
// saving. A schedule written as "evening, 20:00-07:00, brightness 40" and judged against the
// container's clock fires seven hours out — it dims the light at lunchtime and leaves it bright
// all evening — and nothing in the log, the routes or the device's own state says which clock
// was read. It is a display that is wrong and looks healthy, which is the worst shape a defect
// in this module can take. So the zone is a setting, it is required whenever a schedule is
// written, and a zone that will not load refuses the start rather than falling back to UTC:
// a silent fallback is exactly the failure above, with the excuse already written down.

// brightnessWindow is one line of the `brightness_schedule` setting: what the operator writes.
//
// It is a separate type from scheduleWindow below because the two answer different questions.
// This one is the shape of the JSON in the one text area a manifest can offer (a `list` is the
// largest value shape there is, and it renders as a block of JSON); scheduleWindow is the same
// thing after it has been read, checked and turned into minutes, a day mask and a span a log
// line can print.
//
// Brightness is a pointer so that a line which leaves it out is not read as zero. Zero is a
// real answer here — a light switched off overnight is a thing an operator may want — so an
// absent value has to be told apart from a written one, and it is refused rather than guessed.
type brightnessWindow struct {
	// From is when the window opens, as a time of day: 20:00.
	From string `json:"from"`

	// To is when it closes, as a time of day: 07:00. A To earlier than From is a window that
	// wraps midnight, which is the evening window almost everybody writes first.
	To string `json:"to"`

	// Brightness is what the light runs at inside the window, 0-255.
	Brightness *int `json:"brightness"`

	// Days restricts the window to some days of the week: ["sat", "sun"]. Absent or empty
	// covers every day.
	//
	// It is omitted when it holds nothing, which matters in one place only: this is also the shape
	// the store keeps a window in, and a document a person opens should say "every day" by leaving
	// a field out rather than by carrying a null to be looked up.
	Days []string `json:"days,omitempty"`
}

// scheduleWindow is one window after it has been read: the times as minutes since the zone's
// own midnight, the brightness, and the days it covers as a mask.
type scheduleWindow struct {
	// from and to are minutes after midnight, 0 to 1439, in the zone the schedule is judged
	// in. from == to is refused at parse time, so from < to and from > to are the only two
	// shapes left: a span inside one day, and a span that wraps midnight.
	from int
	to   int

	// level is the brightness the light runs at inside this window.
	level int

	// days is the set of weekdays this window covers, one bit per time.Weekday, and zero
	// means every day. Zero is the "nothing written" case, which is why an empty `days` list
	// reads as every day rather than as no day at all.
	days uint8

	// span is "20:00-07:00", as the operator wrote it, for a log line or a refusal.
	span string
}

// brightnessSchedule is the `brightness_schedule` setting and the `timezone` setting together:
// the windows, and the zone they are judged in.
//
// A nil *brightnessSchedule is a real value and means the operator wrote no schedule. Every
// method tolerates it, because "no schedule" is the state every install before this setting was
// in, and the fixed brightness is then the whole answer — which is what keeps this change from
// altering a single write on an instance that does not use it.
type brightnessSchedule struct {
	// windows is the list in the operator's order. The first window that contains the moment
	// decides, and later ones are not consulted: a list is a list, and overlapping windows are
	// not a defect the module invents a rule for beyond saying which one wins.
	windows []scheduleWindow

	// zone is where the windows are read, loaded from the zone database compiled into this
	// binary rather than from the container's filesystem — see the time/tzdata import in
	// main.go for why that distinction is load-bearing.
	zone *time.Location

	// zoneName is the zone as the loader named it, which is what a log line prints: the
	// sentence that makes a seven-hour mistake visible has to say which clock it read.
	zoneName string
}

// level is the brightness in force at one instant, with the sentence a person reads saying
// which rule chose it, and whether a window chose it at all. A nil schedule answers false and
// nothing else: the fixed brightness is then the whole answer, and that is the state every
// install before this setting is in.
func (s *brightnessSchedule) level(when time.Time) (int, string, bool) {
	if s == nil {
		return 0, "", false
	}

	local := when.In(s.zone)
	minute := local.Hour()*60 + local.Minute()

	for i := range s.windows {
		if !s.windows[i].covers(minute, local.Weekday()) {
			continue
		}

		return s.windows[i].level, fmt.Sprintf("the %s window of %s, where it is %s",
			s.windows[i].span, s.zoneName, local.Format("15:04 MST")), true
	}

	return 0, fmt.Sprintf("no window of %s covers %s, so the fixed brightness stands",
		s.zoneName, local.Format("15:04 MST")), false
}

// covers reports whether a window contains one moment, given that moment's minute of the day
// and its weekday in the schedule's own zone.
//
// The rule at the edges, which the tests pin because an hour of a display depends on it:
//
//   - **from is inside and to is outside.** A window written 20:00-07:00 is in force at 20:00
//     and is not in force at 07:00. Half-open, in other words: [20:00, 07:00). The operator
//     reads "evening, until seven" and the light goes back to the fixed brightness on the
//     stroke of seven rather than a minute later.
//   - **a day names the calendar day the moment falls on, in the zone.** A window that wraps
//     midnight with days ["fri"] covers Friday evening up to midnight and not the small hours
//     of Saturday, because those are Saturday. Name both days to have both, and the manifest
//     says so in those words.
func (w scheduleWindow) covers(minute int, day time.Weekday) bool {
	if w.days != 0 && w.days&(1<<uint(day)) == 0 {
		return false
	}

	if w.from < w.to {
		return minute >= w.from && minute < w.to
	}

	// A window that wraps midnight is two spans on one clock: the evening part of the day it
	// opens on, and the small hours of the one it closes on.
	return minute >= w.from || minute < w.to
}

// parseBrightnessSchedule reads the two settings into the one schedule they describe, or nil
// when the operator wrote none.
//
// The zone is required exactly when a schedule is: with no windows the fixed brightness is the
// whole answer and an empty `timezone` costs nothing, which is what lets every install that
// predates this setting keep starting unchanged. A zone that *is* written is checked even with
// no schedule, because a misspelt zone accepted today is a start refused tomorrow — on the save
// that adds the first window, after the operator had been told his zone was fine.
func parseBrightnessSchedule(entries []json.RawMessage, zone string) (*brightnessSchedule, error) {
	named := strings.TrimSpace(zone)

	if len(entries) == 0 {
		if named == "" {
			return nil, nil
		}

		if _, err := loadZone(named); err != nil {
			return nil, err
		}

		return nil, nil
	}

	windows, err := parseScheduleWindows(entries)
	if err != nil {
		return nil, err
	}

	location, err := loadZone(named)
	if err != nil {
		return nil, err
	}

	return &brightnessSchedule{windows: windows, zone: location, zoneName: location.String()}, nil
}

// loadZone turns the `timezone` setting into a location, and refuses an empty or unknown one.
//
// The refusal names the setting and the value, because it is read on the instance by a person
// who has just pasted a schedule and needs to know which of the two fields to fix. What it
// deliberately does not do is fall back to UTC: a schedule judged seven hours out is a light
// that is wrong every evening and says nothing about it, and a fallback would make that the
// module's quiet default rather than a mistake somebody has to correct.
func loadZone(name string) (*time.Location, error) {
	if name == "" {
		return nil, fmt.Errorf(
			"brightness_schedule is written and timezone is empty: name the zone its windows are judged in, as an IANA name such as America/Denver. This process runs inside the Prusa Connect container, whose own clock is almost always UTC, so a schedule with no zone of its own would fire seven hours away from the operator's evening and nothing in the log would say so")
	}

	location, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf(
			"timezone is %q, which is not a zone this build can load: write an IANA name such as America/Denver, because the schedule is judged in the zone you name and not in this process's own clock (%w)",
			name, err)
	}

	return location, nil
}

// parseScheduleWindows reads the `brightness_schedule` list one line at a time.
//
// The lines are decoded one at a time for the reason the bindings list is: a strict decode of a
// whole slice can only say "unknown field", and a person reading that on the instance is left
// counting lines in a text area. One line at a time is what makes the refusal read
// `brightness_schedule[1]: json: unknown field "brigthness"` — the line and the field.
func parseScheduleWindows(entries []json.RawMessage) ([]scheduleWindow, error) {
	windows := make([]scheduleWindow, 0, len(entries))

	for i, raw := range entries {
		var one brightnessWindow

		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()

		if err := decoder.Decode(&one); err != nil {
			return nil, fmt.Errorf(
				"brightness_schedule[%d]: %w: every line carries from, to and brightness, and may carry days",
				i, err)
		}

		window, err := one.window(i)
		if err != nil {
			return nil, err
		}

		windows = append(windows, window)
	}

	return windows, nil
}

// window is one checked line of the list, or the sentence saying what is wrong with it.
//
// It names the line the way the settings list numbers it, and hands the reading to parsed with that
// name: the store's windows are the same concept read from a different place, and this is the one
// function that decides what a window means.
func (b brightnessWindow) window(line int) (scheduleWindow, error) {
	return b.parsed(fmt.Sprintf("brightness_schedule[%d]", line))
}

// parsed is one line of a window list after it has been read and checked, with `where` naming the
// line in whatever list it came from — `brightness_schedule[0]` in the settings, `windows[0]` in
// this module's own store. One reader for the two, so that a window written through a route and a
// window written in the settings file cannot mean two different things.
func (b brightnessWindow) parsed(where string) (scheduleWindow, error) {
	from, err := parseClock(b.From)
	if err != nil {
		return scheduleWindow{}, fmt.Errorf("%s: from %w", where, err)
	}

	to, err := parseClock(b.To)
	if err != nil {
		return scheduleWindow{}, fmt.Errorf("%s: to %w", where, err)
	}

	span := fmt.Sprintf("%02d:%02d-%02d:%02d", from/60, from%60, to/60, to%60)

	if from == to {
		return scheduleWindow{}, fmt.Errorf(
			"%s runs from %s to %s, which is no time at all: a window whose two ends are the same is refused rather than read as a whole day or as nothing, because which one was meant is not something this module can guess",
			where, span[:5], span[6:])
	}

	if b.Brightness == nil {
		return scheduleWindow{}, fmt.Errorf(
			"%s (%s) carries no brightness: every window says what brightness it applies, 0-255",
			where, span)
	}

	if *b.Brightness < 0 || *b.Brightness > 255 {
		return scheduleWindow{}, fmt.Errorf(
			"%s (%s) carries brightness %d, and a WLED controller takes 0-255",
			where, span, *b.Brightness)
	}

	days, err := parseDays(b.Days)
	if err != nil {
		return scheduleWindow{}, fmt.Errorf("%s (%s): %w", where, span, err)
	}

	return scheduleWindow{from: from, to: to, level: *b.Brightness, days: days, span: span}, nil
}

// parseClock reads one end of a window as minutes after midnight.
//
// Both 20:00 and 9:00 are accepted, because a person types both and the alternative is a
// refusal over a leading zero. Seconds, a zone suffix and "24:00" are not: a window's end is a
// time of day, and 24:00 is the midnight that 00:00 already names.
func parseClock(value string) (int, error) {
	written := strings.TrimSpace(value)
	if written == "" {
		return 0, fmt.Errorf("is empty: write the time of day, HH:MM")
	}

	hours, minutes, found := strings.Cut(written, ":")
	if !found {
		return 0, fmt.Errorf("is %q, and a time of day is written HH:MM, such as 20:00", written)
	}

	hour, hourErr := strconv.Atoi(hours)
	minute, minuteErr := strconv.Atoi(minutes)

	if hourErr != nil || minuteErr != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, fmt.Errorf("is %q, and a time of day is written HH:MM between 00:00 and 23:59", written)
	}

	return hour*60 + minute, nil
}

// parseDays reads the optional `days` list into a mask of weekdays, with zero meaning every
// day.
//
// Three-letter names only, because that is what a person writes in a list this short, and a
// name outside the seven is refused with the field named: a window silently restricted to no
// day at all would be a schedule that never fires and never says why.
func parseDays(names []string) (uint8, error) {
	if len(names) == 0 {
		return 0, nil
	}

	var mask uint8

	for _, name := range names {
		day, ok := weekdays[strings.ToLower(strings.TrimSpace(name))]
		if !ok {
			return 0, fmt.Errorf("days names %q, and a day is mon, tue, wed, thu, fri, sat or sun", name)
		}

		mask |= 1 << uint(day)
	}

	return mask, nil
}

// weekdays is the vocabulary of a window's `days` field, as time.Weekday values so that a
// moment's own weekday can be tested against the mask without arithmetic in between.
//
//nolint:gochecknoglobals // a literal table, read and never written.
var weekdays = map[string]time.Weekday{
	"sun": time.Sunday,
	"mon": time.Monday,
	"tue": time.Tuesday,
	"wed": time.Wednesday,
	"thu": time.Thursday,
	"fri": time.Friday,
	"sat": time.Saturday,
}
