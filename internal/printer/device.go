package printer

import "github.com/prusa3d/prusa-connect/sdk/deviceapi"

// RecordFromDevice reads the parts of a device the host answered with that this
// package tracks.
//
// Everything else a Device carries is left behind on purpose: the capabilities,
// the tags, the firmware and the dialog are facts about the machine rather than
// readings of what it is doing, and a caller that wants them has the Device in its
// hand already. The state tree is handed over as it came — this package accumulates
// it and does not rewrite it.
func RecordFromDevice(device deviceapi.Device) Record {
	record := Record{
		ID:        device.ID,
		Name:      device.Name,
		Connected: device.Connected,
		Retired:   device.Retired,
		State:     device.State,
	}

	for _, issue := range device.Issues {
		record.Issues = append(record.Issues, Issue{
			Reason:   issue.Reason,
			Severity: issue.Severity,
			Detail:   issue.Detail,
		})
	}

	return record
}
