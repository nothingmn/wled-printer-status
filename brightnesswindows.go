// The brightness windows, as the Brightness screen edits them: a list of spans of the day and the
// brightness to run at inside each.
//
// # This is the same concept the `brightness_schedule` setting carries, and not a second one
//
// schedule.go already reads a list of windows out of the settings pane, judges them in the zone the
// `timezone` setting names, and hands the answer to the writer's retune. docs/product-requirements.md
// §5 item 7 asks for that list to be edited by route rather than by pasting JSON, which is a change
// of *where the list is kept* and not a change to what a window means. So the store holds the same
// shape the setting holds — `brightnessWindow`, the same type, the same `from`/`to`/`brightness`/
// `days` — and it is read by the same function, `brightnessWindow.parsed`, which is the one place in
// this module that decides what a window means. A window written through a route and a window
// written in the settings file therefore cannot drift apart: they are the same reading.
//
// # Which clock, and why this one
//
// **A window is judged in the IANA zone the `timezone` setting names, never in this process's own
// clock.** That is not a preference: the process runs inside the Prusa Connect container, whose clock
// is almost always UTC, and the operator lives at UTC-7 in a zone that also observes daylight saving.
// A window written "evening, 20:00-07:00, brightness 40" and judged against the container's clock
// fires seven hours out — it dims the light at lunchtime and leaves it bright all evening — and
// nothing in the log, the routes or the device's own state says which clock was read. schedule.go
// states the same rule at length, §2.8 makes the zone the one thing the operator sets, and a
// brightness window an hour wrong is worse than none.
//
// So there is no fallback. With no zone named, a window cannot be written and cannot be judged; the
// refusal is loadZone's own sentence, naming the setting, and the read says which zone it would have
// used and that it had none.
//
// # What is refused, and what is not
//
//   - **A window whose two ends are the same** is refused: `20:00-20:00` is either a whole day or no
//     time at all, and which one was meant is not something this module can guess.
//   - **A window whose end precedes its start wraps midnight** and is accepted. `20:00-07:00` is the
//     evening window almost everybody writes first, and refusing it would make the most common window
//     unrepresentable. It is reported with `wraps_midnight: true` so that a screen can show it was
//     understood rather than mangled.
//   - **Overlapping windows are allowed.** schedule.go's own rule: "a list is a list, and overlapping
//     windows are not a defect the module invents a rule for beyond saying which one wins". The first
//     window that contains the moment decides, later ones are not consulted, and the answer says
//     which index won so that two overlapping windows are never a mystery.
//   - **A brightness outside 0-255** is refused, because that is the range a WLED controller takes.
//   - **`days` is carried, not required.** No mockup asks for a day-of-week control, so nothing here
//     requires one and a window with no `days` covers every day. The field exists in the setting
//     today and is read by the same parser, and dropping it from the store would lose a capability
//     the module already has — so it travels through untouched rather than being removed.
package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// windowRow is one stored window as it stands: the row a person wrote, and the window it means once
// the shared parser has read it.
type windowRow struct {
	// stored is the row exactly as the document holds it, which is what an answer says was written.
	stored brightnessWindow

	// parsed is what that row means: the ends as minutes after midnight, the brightness, the day
	// mask and the span for a log line. It comes from brightnessWindow.parsed, the same function the
	// settings list is read by.
	parsed scheduleWindow
}

// windowsOf is the document's window list as it stands, in the operator's order, with every window
// checked.
//
// A window that cannot be acted on refuses the whole read rather than being skipped: a list with a
// bad line in it is a list a person has to look at, and quietly dropping one would leave a window
// that never fires and never says why. The refusal names the line with `windows[i]`, the same shape
// the settings list's refusal uses, so that a person opening config.json knows which one to fix.
func windowsOf(config storedConfig) ([]windowRow, error) {
	rows := make([]windowRow, 0, len(config.Windows))

	for i, one := range config.Windows {
		parsed, err := one.parsed(fmt.Sprintf("windows[%d]", i))
		if err != nil {
			return nil, err
		}

		rows = append(rows, windowRow{stored: one, parsed: parsed})
	}

	return rows, nil
}

// windowInForce is the index of the first window that contains a moment, and false when none does.
//
// The first, and not the tightest or the last: schedule.go's `level` walks the list in the operator's
// order and takes the first that covers, and a route that picked a different one would be telling a
// screen a different story from the one the light would follow. The answer carries the index so that
// two overlapping windows are never ambiguous.
func windowInForce(rows []windowRow, zone *time.Location, at time.Time) (int, bool) {
	local := at.In(zone)
	minute := local.Hour()*60 + local.Minute()

	for i := range rows {
		if rows[i].parsed.covers(minute, local.Weekday()) {
			return i, true
		}
	}

	return 0, false
}

// wrapped says whether a window's end precedes its start, which is the window that spans midnight.
func (w scheduleWindow) wrapped() bool { return w.from > w.to }

// dayNames is a window's day mask as the three-letter names it was written with, in week order, and
// an empty list for a window that covers every day.
//
// It is derived from the mask rather than echoed from the stored slice so that the answer reports
// what the window *means* — a list naming monday twice is one day, and a screen that showed it twice
// would be showing a window that is not the one in force.
func dayNames(mask uint8) []string {
	if mask == 0 {
		return []string{}
	}

	names := make([]string, 0, 7)

	for _, name := range []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"} {
		if mask&(1<<uint(weekdays[name])) != 0 {
			names = append(names, name)
		}
	}

	return names
}

// windowAnswer is one window as a caller reads it: what was written, what it means, and whether it
// is the one in force at the instant the answer was made.
type windowAnswer struct {
	// Index is the window's position in the list, counting from zero, and it is the name these
	// routes address a window by: PUT and DELETE take it. A position rather than a minted id,
	// because the order is part of what a window list means — the first one that contains the
	// moment decides — so the position is a name and not an accident of storage.
	Index int `json:"index"`

	From       string   `json:"from"`
	To         string   `json:"to"`
	Brightness int      `json:"brightness"`
	Days       []string `json:"days"`

	// Span is the two ends as the parser normalised them, and WrapsMidnight says the end precedes
	// the start, which is the window that spans midnight rather than a mistake.
	Span          string `json:"span"`
	WrapsMidnight bool   `json:"wraps_midnight"`

	// Active says whether this window contains the instant the answer was made, in the zone named
	// beside it. It is absent when there is no zone to judge in: reporting false there would be
	// claiming a window is out of force when nothing has looked at a clock at all.
	Active *bool `json:"active,omitempty"`

	// Wins is true on the one window the list would follow at that instant. More than one window may
	// be active when they overlap; exactly one wins, and it is the first.
	Wins bool `json:"wins"`
}

// windowsAnswer is the whole list, and what a caller has to know to read it.
type windowsAnswer struct {
	Windows []windowAnswer `json:"windows"`
	Count   int            `json:"count"`

	// JudgedIn is the IANA zone the windows are read in — the `timezone` setting — and Now is the
	// instant they were judged at, written in that zone. Both are always present, because a
	// brightness window an hour wrong is worse than none and these two are how a person checks.
	JudgedIn string `json:"judged_in"`
	Now      string `json:"now"`

	// Wins is the index of the window in force at Now, absent when none is, and Level is the
	// brightness it would ask for.
	Wins  *int `json:"wins,omitempty"`
	Level *int `json:"level,omitempty"`

	Persisted bool   `json:"persisted"`
	Note      string `json:"note,omitempty"`
}

// windowsAnswerOf renders the list, judging every window against one instant in one zone.
//
// zone may be nil, which is a module whose `timezone` setting names nothing: the windows are then
// listed as written and nothing claims to know which of them is in force.
func windowsAnswerOf(rows []windowRow, zone *time.Location, zoneName string, at time.Time, available bool) windowsAnswer {
	said := windowsAnswer{
		Windows:   make([]windowAnswer, 0, len(rows)),
		Count:     len(rows),
		JudgedIn:  zoneName,
		Now:       at.In(zoneOrUTC(zone)).Format(time.RFC3339),
		Persisted: available,
	}

	winner, any := -1, false

	if zone != nil {
		winner, any = windowInForce(rows, zone, at)
	}

	for i, row := range rows {
		one := windowAnswer{
			Index:         i,
			From:          row.stored.From,
			To:            row.stored.To,
			Brightness:    row.parsed.level,
			Days:          dayNames(row.parsed.days),
			Span:          row.parsed.span,
			WrapsMidnight: row.parsed.wrapped(),
			Wins:          any && i == winner,
		}

		if zone != nil {
			active := row.parsed.covers(localMinute(at, zone), at.In(zone).Weekday())
			one.Active = &active
		}

		said.Windows = append(said.Windows, one)
	}

	if any {
		index := winner
		level := rows[winner].parsed.level
		said.Wins = &index
		said.Level = &level
	}

	return said
}

// localMinute is one instant as minutes after midnight in a zone, which is the unit a window is
// written in.
func localMinute(at time.Time, zone *time.Location) int {
	local := at.In(zone)

	return local.Hour()*60 + local.Minute()
}

// zoneOrUTC is a zone, or UTC for a module that named none. It is used only to render an instant for
// a person to read, never to judge a window: judging is the thing that must not fall back.
func zoneOrUTC(zone *time.Location) *time.Location {
	if zone == nil {
		return time.UTC
	}

	return zone
}

// windowsZonedNote is what every answer about windows says when this module holds no zone to judge
// them in, and it says why rather than falling back to a clock that is almost certainly not the
// operator's.
const windowsZonedNote = "This module holds no zone, so no window is being judged: set one on the Overview screen, " +
	"as an IANA name such as America/Denver, because this process runs inside the Prusa Connect container whose " +
	"clock is almost always UTC and a window judged there fires hours away from the evening it was written for."

// windowsStoredNote is what every write to the window list says: this list is what the light follows,
// and `brightness_schedule` is the fallback it was migrated from — read only while this list is empty,
// which is what makes an install whose windows were pasted into the settings go on running unchanged.
const windowsStoredNote = "This list is what the light's brightness follows whenever it holds a window: the first " +
	"window that contains the moment decides, and the fixed brightness stands where none does. brightness_schedule " +
	"is the fallback it was migrated from, read only while this list is empty. Both are judged in the zone this " +
	"module holds, and never in this process's own clock."

// windowsBorrowedNote is the one fact about windows that a whole-controller list hides, and it is
// worth stating on every answer: a brightness write is controller-wide, so a window only ever lands
// on a binding that drives its whole light. A borrowed zone's brightness belongs to the strip's owner
// and this module never writes one.
const windowsBorrowedNote = "A brightness is controller-wide, so a window lands only on a binding that drives its " +
	"whole light. A binding that borrows one segment of somebody else's strip never has its brightness written, " +
	"and this module will not dim a light it shares."

// quotedWindows is the list of what `brightness` accepts, for a refusal a person reads.
func quotedWindows() string {
	return "from and to as a time of day written HH:MM, brightness as 0-255, and days optionally as mon, tue, wed, thu, fri, sat or sun"
}

// checkWindowRow is the refusal for a row that does not parse, with the settings' own sentence and
// the store's own line name. It exists so that the route reports the same words as the parser rather
// than a second description of them that could drift.
func checkWindowRow(one brightnessWindow, where string) (scheduleWindow, error) {
	parsed, err := one.parsed(where)
	if err != nil {
		return scheduleWindow{}, fmt.Errorf("%w: %s", err, quotedWindows())
	}

	return parsed, nil
}

// windowIndexOf reads the index a route names, and refuses one that is not a window in the list.
func windowIndexOf(rows []windowRow, named string) (int, error) {
	wanted := strings.TrimSpace(named)

	index, err := strconv.Atoi(wanted)
	if err != nil {
		return 0, fmt.Errorf(
			"%q is not a window's position: windows are addressed by their index, counting from zero, as the list reports it",
			wanted)
	}

	if index < 0 || index >= len(rows) {
		if len(rows) == 0 {
			return 0, fmt.Errorf("%d is not a window's position: the list holds no windows at all", index)
		}

		return 0, fmt.Errorf("%d is not a window's position: the list holds %d windows, and the positions are 0 to %d",
			index, len(rows), len(rows)-1)
	}

	return index, nil
}
