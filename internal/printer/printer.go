package printer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// StaleAfter is how old the newest change about a printer may be before this
// package stops calling its readings present tense.
//
// The host publishes a path only when it moves, measured at thirteen paths
// every two seconds while a printer prints. Thirty seconds is fifteen of those
// bursts: far enough past a healthy cadence that a quiet stream means something
// is wrong, and close enough that a display stops claiming a job is running
// long before anyone would walk over to look. A printer that is simply idle
// reports nothing, so it goes stale too, which is why Stale clears the job and
// not the connection: "IDLE, and nothing has moved for an hour" is a true
// sentence, and "printing, 94%" is not.
const StaleAfter = 30 * time.Second

// errNoValue reports that a printer wrote null to a path: it has no value there
// now. It is not a failure, and it is not a zero — it is the printer taking a
// value away, which is the one thing a state tree cannot otherwise say.
var errNoValue = errors.New("the printer reported no value")

// ValueError names one path whose value this build could not read.
//
// It is not fatal. A driver may spell a number as text, a newer firmware may
// answer with a word this build has no reading of, and a printer may report a
// negative remaining time as a sentinel. None of those is a reason to lose the
// path, the batch or the subscription: Apply keeps the value it last read for
// that path and returns this, so the caller can log what it refused to believe.
type ValueError struct {
	// Path is the node the value arrived on.
	Path string
	// Raw is the value as the host published it, cut to a length a log line
	// holds. The whole of it is in State.tree if the caller needs it.
	Raw json.RawMessage
	// Err is why this build could not read it.
	Err error
}

// Error renders the refusal as one line naming the path and what was wrong.
func (e *ValueError) Error() string {
	return fmt.Sprintf("printer: %s: %v", e.Path, e.Err)
}

// Unwrap returns the reason, so a caller can test it with errors.Is.
func (e *ValueError) Unwrap() error { return e.Err }

// Reading is one value a printer reported, with whether it reported one at all.
//
// The zero value is unknown, and unknown is not zero. A path the printer has
// never written, a path it wrote null to, and a value this build cannot read all
// leave the zero value here. Nothing in this package turns "said nothing" into
// 0, "", IDLE or false, because a display cannot tell an invented zero from a
// measured one.
type Reading[T any] struct {
	// Value is what the printer reported. It is the zero value of T when
	// Known is false, and carries no meaning then.
	Value T
	// Known reports whether the printer reported a value this build could read.
	Known bool
}

// Update is one path of the state tree and the value it now carries: one patch
// out of a telemetry window, with the envelope already unwrapped.
type Update struct {
	// Path is the node's address in the tree, such as /job/progress.
	Path string
	// Value is the node's JSON value, exactly as the host published it.
	Value json.RawMessage
}

// Issue is one thing the host found wrong with the printer, as the device
// listing carries it.
type Issue struct {
	// Reason is the token a caller branches on.
	Reason string
	// Severity is info, warning or error, and the reason decides it.
	Severity string
	// Detail is the machine's own words, such as the title of a dialog. Show it
	// to a person; do not branch on it.
	Detail string
}

// Record is the typed device record this package folds in: the part of one
// deviceapi.Device it tracks, with no sdk type in it, so the reducer can be
// tested without one. RecordFromDevice builds it from what the host answered.
type Record struct {
	// ID and Name are the printer's identity: the host's id, which survives a
	// reconnect, and the name an operator gave it.
	ID   string
	Name string

	// Connected is the host's answer about a session, and the only signal that
	// a printer is gone. False means State is the last tree it reported, which
	// nothing in the host dates.
	Connected bool

	// Retired is true once the id no longer names a machine: the printer was
	// removed, or registered again under a new id. A retired printer never
	// comes back, so whatever was kept for it can be let go.
	Retired bool

	// State is the whole state tree the host holds for the printer, keyed by
	// path. It may be nil, and often is for a device that has just been
	// admitted: the record exists before the tree does.
	State map[string]json.RawMessage

	// Issues is what the host found wrong with it, worst first, and empty when
	// nothing is.
	Issues []Issue
}

// State is everything this package holds about one printer: the accumulated
// state tree, the record last read, and the moment the tree last changed.
//
// It is a value. Apply returns a new one and never writes the one it was given,
// so a caller may keep the previous state to compare against. The zero State is
// valid and empty: nothing read, nothing reported, nothing claimed.
//
// The tree here is an accumulation and not a snapshot. The host publishes a path
// only when it moves, so a batch of windows says what changed and never what the
// whole printer is; the accumulation is this type's job, and it starts empty.
type State struct {
	record     Record
	tree       map[string]json.RawMessage
	lastChange time.Time
}

// Apply folds one device record and one batch of telemetry into the next state.
//
// record is what the host answered for the printer, and its tree is applied
// first: it is the whole tree the host holds, which is the newest value of every
// path it kept. updates are applied over it, because a window handed over with
// the same call carries values the read may not have seen. at is the moment the
// batch is applied; it is a parameter rather than a call to time.Now so a test
// can drive the clock, and lastChange is left where it is when at is behind it.
//
// A value that changed moves lastChange. A read that says nothing moved does
// not: the clock measures the last change, and a read is not a change. That is
// what makes Stale honest — a printer the host keeps answering for, whose tree
// has not moved in half a minute, is a printer this package will not call
// printing.
//
// The returned error is nil, or a *ValueError per value this build could not
// read, joined. The state is still returned with every other path folded in: one
// unreadable number is one path, not one failed batch.
func Apply(previous State, record Record, updates []Update, at time.Time) (State, error) {
	next := State{
		record:     copyRecord(record),
		tree:       make(map[string]json.RawMessage, len(previous.tree)+len(record.State)+len(updates)),
		lastChange: previous.lastChange,
	}

	for path, value := range previous.tree {
		next.tree[path] = value
	}

	var problems []error

	for path, value := range record.State {
		if err := next.put(path, value, at); err != nil {
			problems = append(problems, err)
		}
	}

	for _, update := range updates {
		if err := next.put(update.Path, update.Value, at); err != nil {
			problems = append(problems, err)
		}
	}

	return next, errors.Join(problems...)
}

// WithUpdates folds one batch into the state it already carries and keeps the
// record it already holds. It is what the telemetry path calls, where nothing
// new was read from the host.
func (s State) WithUpdates(updates []Update, at time.Time) (State, error) {
	return Apply(s, s.record, updates, at)
}

// WithRecord folds one device read into the state it already carries, with no
// telemetry beside it. It is what the periodic read calls.
func (s State) WithRecord(record Record, at time.Time) (State, error) {
	return Apply(s, record, nil, at)
}

// put records one path and moves the change clock when the value differs from
// what was held.
//
// A path that is not an address in the tree — empty, or not rooted at a slash —
// is not a fact about this printer and is dropped without an error, which is what
// the host's own readers do. A value this build cannot read is not stored: the
// tree keeps what it had and the error names the path.
func (s *State) put(path string, value json.RawMessage, at time.Time) error {
	if path == "" || path[0] != '/' || value == nil {
		return nil
	}

	if err := checkValue(path, value); err != nil && !errors.Is(err, errNoValue) {
		return &ValueError{Path: path, Raw: clip(value), Err: err}
	}

	if held, ok := s.tree[path]; ok && bytes.Equal(held, value) {
		return nil
	}

	s.tree[path] = append(json.RawMessage(nil), value...)

	if s.lastChange.IsZero() || at.After(s.lastChange) {
		s.lastChange = at
	}

	return nil
}

// Snapshot is what this package says about the printer at one moment.
//
// It is a statement of facts with their age, not a rendering. Two of its fields
// are the whole point: Connected, which is the host's word on whether the printer
// is there, and LastChange, which is when anything about it actually moved. A
// caller that renders Job.Progress without looking at both is the caller this
// package was written to stop.
type Snapshot struct {
	// ID and Name are the printer's identity, and empty before a record has been
	// read.
	ID   string
	Name string

	// Connected is the host's answer: a session is open right now. False means
	// every reading below is the last the printer reported, at LastChange.
	Connected bool

	// Retired is true once the id no longer names a machine. A retired printer
	// never comes back.
	Retired bool

	// State is the state machine value the printer last reported, such as
	// PRINTING. It is unknown when the printer has reported none, and it keeps
	// saying PRINTING for a printer that is gone: that is the host's memory, not
	// a reading. Connected is what decides whether it is present tense.
	State Reading[string]

	// Job is what the printer last said about its job.
	Job Job

	// Nozzle and Bed are the temperatures of head "0" and of the bed, in degrees
	// Celsius, each with the target it is heating to.
	Nozzle Temperature
	Bed    Temperature

	// Material is the short name of the material loaded in head "0", such as
	// PLA. It is known and empty for a tool with nothing loaded, and unknown for
	// a tool that said nothing: a display can tell an empty spool holder from a
	// printer that has never mentioned material.
	Material Reading[string]

	// LastChange is when the last update that actually changed a value arrived,
	// in UTC. It is the zero time before anything has changed.
	LastChange time.Time

	// ObservedAt is when this snapshot was taken, in UTC.
	ObservedAt time.Time

	// Age is ObservedAt minus LastChange: how old the newest fact here is. It is
	// zero when nothing has ever been reported, and never negative.
	Age time.Duration

	// Stale reports that the newest fact is older than StaleAfter, so no reading
	// here may be presented as describing the printer now. It is true for a
	// printer nothing has ever been reported about.
	Stale bool

	// Issues is what the host found wrong with the printer, worst first.
	Issues []Issue

	// Tree is every path the printer has reported, as it reported it, keyed by
	// path. It is a copy: a caller may keep it or change it.
	//
	// A caller that reads a path out of here is reading a value the host
	// published, which may be older than LastChange says about the rest of the
	// tree and may be null. Nothing in here is interpreted; the fields above are
	// the paths this build has a reading of.
	Tree map[string]json.RawMessage
}

// Snapshot reads the state as it stands at one moment.
//
// at decides both the age and staleness, so a caller with its own clock can ask
// what the snapshot would have said then. The result owns its own copies: no map
// or slice in it is shared with the state, so a caller may keep it across further
// batches.
func (s State) Snapshot(at time.Time) Snapshot {
	at = at.UTC()

	snapshot := Snapshot{
		ID:         s.record.ID,
		Name:       s.record.Name,
		Connected:  s.record.Connected,
		Retired:    s.record.Retired,
		State:      textReading(s.tree, PathState),
		Nozzle:     Temperature{Now: numberReading(s.tree, PathNozzleTemp), Target: numberReading(s.tree, PathNozzleTarget)},
		Bed:        Temperature{Now: numberReading(s.tree, PathBedTemp), Target: numberReading(s.tree, PathBedTarget)},
		Material:   textReading(s.tree, PathMaterial),
		LastChange: s.lastChange.UTC(),
		ObservedAt: at,
		Issues:     copyIssues(s.record.Issues),
		Tree:       copyTree(s.tree),
	}

	if !s.lastChange.IsZero() {
		snapshot.Age = at.Sub(s.lastChange)

		if snapshot.Age < 0 {
			// The clock moved back under a batch already applied. A negative age
			// would read as fresher than the moment it was taken, which is worse
			// than a zero.
			snapshot.Age = 0
		}
	}

	snapshot.Stale = s.lastChange.IsZero() || snapshot.Age > StaleAfter
	snapshot.Job = jobOf(s.tree, snapshot.Connected, snapshot.Stale, snapshot.State)

	return snapshot
}

// Job is what the printer last reported about its job.
//
// Every reading here is a fact with an age, never a claim about now. Whether the
// numbers describe the printer at this moment is Live, and Live is false when the
// printer is not connected or the newest fact is stale.
type Job struct {
	// Live reports that the readings below describe the printer now: it is
	// connected and its newest fact is younger than StaleAfter.
	//
	// False does not mean the fields are wrong. It means they are a memory, and
	// that rendering them as the present — "94%, six minutes left" — would be a
	// lie. Snapshot.Age says how old the memory is and Snapshot.LastChange when
	// it was taken.
	Live bool

	// State is the machine's word for what the print is doing, for a machine that
	// reports one apart from its own state. Most do not, and then it is unknown
	// and Snapshot.State is the word that stands.
	State Reading[string]

	// Progress is how far the print has got, in whole percent, as the machine
	// reported it.
	Progress Reading[float64]

	// Remaining is how long the machine says is left. The machine reports whole
	// seconds; this is that count as a duration.
	Remaining Reading[time.Duration]

	// Printing is how long the machine says it has been printing.
	Printing Reading[time.Duration]

	// Display is the name the machine shows for the file, reported only when it
	// shortened the name. Path is the file on the machine's own storage.
	Display Reading[string]
	Path    Reading[string]

	// Running is this package's reading of the last state the printer reported:
	// whether a job is on the bed now. It is derived and not reported, which is
	// why it is a Reading of its own: a word this build does not know leaves it
	// unknown rather than false.
	Running Reading[bool]
}

// Temperature is one heater's reading and the target it is heating to, in degrees
// Celsius. A target of zero is a heater that is off, as the state tree documents
// it; an unknown target is unknown, and the two are not the same thing.
type Temperature struct {
	Now    Reading[float64]
	Target Reading[float64]
}

// LiveProgress reports the job's progress and whether it describes the printer
// now.
//
// It answers false unless the printer is connected, its newest fact is younger
// than StaleAfter, its last reported state says a job is running, and a progress
// has actually been reported. It is the pair to render from: there is no way to
// reach a progress figure through it that is not present tense, so a caller
// cannot accidentally show "94%" for a printer that was switched off.
func (s Snapshot) LiveProgress() (float64, bool) {
	if !s.liveJob() || !s.Job.Progress.Known {
		return 0, false
	}

	return s.Job.Progress.Value, true
}

// LiveRemaining reports the job's remaining time and whether it describes the
// printer now, on the same terms as LiveProgress.
func (s Snapshot) LiveRemaining() (time.Duration, bool) {
	if !s.liveJob() || !s.Job.Remaining.Known {
		return 0, false
	}

	return s.Job.Remaining.Value, true
}

// liveJob reports whether the job readings describe the printer at this moment.
func (s Snapshot) liveJob() bool {
	return s.Job.Live && s.Job.Running.Known && s.Job.Running.Value
}

// Terminal reports whether the printer's last word about a job says that job is
// over: it finished, or it was stopped.
//
// A terminal word is a fact no later reading can take back, which is why it
// matters to a caller holding Stale. "Printing, 94 %" can be made false by a
// printer going quiet; "finished" cannot. A machine that has said FINISHED and
// then says nothing has nothing new to say, and a display is right to keep
// showing it. Every other word — the four with a job on the bed, and the words
// that leave the printer's activity open such as IDLE, READY, ERROR, UNKNOWN or
// one this build has no reading of — can still be overtaken by a reading this
// module has not received, so silence about those is doubt and not quiet.
//
// The word that stands is the job's own when the machine reports one apart from
// its state, exactly as Running reads it.
func (s Snapshot) Terminal() bool {
	state := standingWord(s.Job.State, s.State)
	if !state.Known {
		return false
	}

	return state.Value == StateFinished || state.Value == StateStopped
}

// jobOf reads the job out of the tree and decides whether it is present tense.
func jobOf(tree map[string]json.RawMessage, connected, stale bool, state Reading[string]) Job {
	job := Job{
		Live:      connected && !stale,
		State:     textReading(tree, PathJobState),
		Progress:  numberReading(tree, PathJobProgress),
		Remaining: secondsReading(tree, PathJobRemaining),
		Printing:  secondsReading(tree, PathJobPrinting),
		Display:   textReading(tree, PathJobDisplay),
		Path:      textReading(tree, PathJobPath),
	}

	job.Running = runningJob(job.State, state)

	return job
}

// runningJob reads whether a job is on the bed from the last state the printer
// reported, preferring the job's own word over the machine's.
//
// BUSY, PRINTING, PAUSED and ATTENTION are the four states in which a job is on
// the bed and its progress still describes that job: BUSY is a machine heating or
// calibrating for the print it has taken, and ATTENTION is one waiting on a
// person with the print still there. IDLE, READY, FINISHED and STOPPED are a
// machine with nothing in progress, and ERROR and UNKNOWN say nothing about a
// job. A word this build has never seen leaves the reading unknown rather than
// false, because a firmware that added a state did not thereby remove the job.
func runningJob(jobState, printerState Reading[string]) Reading[bool] {
	state := standingWord(jobState, printerState)
	if !state.Known {
		return Reading[bool]{}
	}

	switch state.Value {
	case StateBusy, StatePrinting, StatePaused, StateAttention:
		return Reading[bool]{Value: true, Known: true}
	case StateIdle, StateReady, StateFinished, StateStopped, StateError, StateUnknown:
		return Reading[bool]{Value: false, Known: true}
	}

	return Reading[bool]{}
}

// standingWord is the one word that describes the printer: the job's own when the
// machine reports one apart from its state, and otherwise the machine's. It is
// the same choice Running and Terminal both make, so "a job is on the bed" and
// "the job is over" cannot be read off two different words.
func standingWord(jobState, printerState Reading[string]) Reading[string] {
	if jobState.Known {
		return jobState
	}

	return printerState
}

// copyRecord takes the record's own copy of the slices a caller handed in, so a
// caller reusing its Device cannot change a state it already folded.
func copyRecord(record Record) Record {
	held := Record{
		ID:        record.ID,
		Name:      record.Name,
		Connected: record.Connected,
		Retired:   record.Retired,
		State:     make(map[string]json.RawMessage, len(record.State)),
		Issues:    copyIssues(record.Issues),
	}

	for path, value := range record.State {
		held.State[path] = append(json.RawMessage(nil), value...)
	}

	return held
}

// copyTree copies every path and its bytes, so a caller holding a snapshot cannot
// reach into the state that produced it.
func copyTree(tree map[string]json.RawMessage) map[string]json.RawMessage {
	if len(tree) == 0 {
		return nil
	}

	out := make(map[string]json.RawMessage, len(tree))

	for path, value := range tree {
		out[path] = append(json.RawMessage(nil), value...)
	}

	return out
}

// copyIssues copies the list, and nil for an empty one so a caller can test it
// with a length.
func copyIssues(issues []Issue) []Issue {
	if len(issues) == 0 {
		return nil
	}

	return append([]Issue(nil), issues...)
}
