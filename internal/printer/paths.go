package printer

import "github.com/prusa3d/prusa-connect/sdk/connectorapi"

// The paths this package reads out of the state tree. Everything else the
// printer reports is kept as it came and is never interpreted here.
//
// The job and tool members are the sdk's own constants rather than copies of
// their spelling, so a path a driver is held to and a path this package reads
// cannot drift. The two that the sdk names only in a driver's map —
// /temperature/bed and /temperature/bed_target — are spelled here, and
// /state carries the state machine value, which the sdk describes rather than
// declares.
const (
	// PathState is the state machine value: IDLE, BUSY, READY, PRINTING,
	// PAUSED, FINISHED, STOPPED, ATTENTION, ERROR or UNKNOWN.
	PathState = "/state"

	// PathJobState is what the print is doing, for a machine that reports one
	// apart from its own state. A machine that does not leaves it absent, and
	// then PathState is the word that stands.
	PathJobState = connectorapi.JobStatePath
	// PathJobProgress is how far the print has got, in whole percent.
	PathJobProgress = connectorapi.JobProgressPath
	// PathJobRemaining is how long the machine says is left, in seconds.
	PathJobRemaining = connectorapi.JobTimeRemainingPath
	// PathJobPrinting is how long the machine says it has been printing, in
	// seconds.
	PathJobPrinting = connectorapi.JobTimePrintingPath
	// PathJobDisplay is the name the machine shows for the file it is printing,
	// reported only when it shortened the name.
	PathJobDisplay = connectorapi.JobDisplayPath
	// PathJobPath is the file the machine is printing, on its own storage.
	PathJobPath = connectorapi.JobPathPath

	// PathNozzleTemp and PathNozzleTarget are the temperature of head "0" and
	// the temperature it is heating to, in degrees Celsius. A machine with one
	// nozzle reports both under head "0", which is the head this package reads:
	// one printer at a time is what the module carries.
	PathNozzleTemp   = "/tools/0/" + connectorapi.NozzleTemp
	PathNozzleTarget = "/tools/0/" + connectorapi.NozzleTargetTemp

	// PathBedTemp and PathBedTarget are the bed's temperature and its target in
	// degrees Celsius.
	PathBedTemp   = "/temperature/bed"
	PathBedTarget = "/temperature/bed_target"

	// PathMaterial is the short name of the material loaded in head "0", such as
	// PLA. It is empty text for a tool with nothing loaded, and absent for a
	// tool that said nothing — which is not the same reading.
	PathMaterial = "/tools/0/" + connectorapi.MaterialAbbreviation

	// PathAxisZ is how far the nozzle has travelled up the print, in millimetres,
	// as the machine's own telemetry reports it (`axis_z`, which the xbuddy
	// connector maps here — see internal/connector/xbuddy/inbound.go).
	//
	// The sdk names the build volume's Z and no axis position, so this is spelled
	// here for the reason /temperature/bed is: the path exists in the driver's map
	// rather than in the sdk's constants.
	//
	// **There is no layer number anywhere in the state tree.** The machine reports a
	// position and not a count of layers, so a panel that says where a print has got
	// to says this and not "L42": a layer number would have to be derived from a
	// layer height nothing here knows.
	PathAxisZ = "/axis/z"
)

// The state machine values PathState and PathJobState carry. They are named
// here because the snapshot's reading of them is a table, and a table written
// against string literals is a table nobody can check.
const (
	StateIdle      = "IDLE"
	StateBusy      = "BUSY"
	StateReady     = "READY"
	StatePrinting  = "PRINTING"
	StatePaused    = "PAUSED"
	StateFinished  = "FINISHED"
	StateStopped   = "STOPPED"
	StateAttention = "ATTENTION"
	StateError     = "ERROR"
	StateUnknown   = "UNKNOWN"
)
