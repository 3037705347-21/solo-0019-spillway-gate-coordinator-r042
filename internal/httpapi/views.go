package httpapi

import (
	"time"

	"example.com/spillway-gate-coordinator/internal/contracts"
	"example.com/spillway-gate-coordinator/internal/domain"
)

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func gateView(gate domain.Gate) contracts.GateView {
	return contracts.GateView{
		ID:              gate.ID,
		Name:            gate.Name,
		Status:          string(gate.Status),
		Aperture:        gate.Aperture,
		MaxAperture:     gate.MaxAperture,
		ActiveCommandID: gate.ActiveCommandID,
		UpdatedAt:       formatTime(gate.UpdatedAt),
		Version:         gate.Version,
	}
}

func commandView(command domain.GateCommand, includeToken bool) contracts.CommandView {
	view := contracts.CommandView{
		ID:               command.ID,
		GateID:           command.GateID,
		TargetAperture:   command.TargetAperture,
		StartingAperture: command.StartingAperture,
		Reason:           command.Reason,
		RequestedBy:      command.RequestedBy,
		Status:           string(command.Status),
		Priority:         command.Priority,
		PriorityBasis:    command.PriorityBasis,
		ExecutedBy:       command.ExecutedBy,
		TerminalReason:   command.TerminalReason,
		CreatedAt:        formatTime(command.CreatedAt),
		UpdatedAt:        formatTime(command.UpdatedAt),
		Version:          command.Version,
	}

	if includeToken && command.Status == domain.CommandApproved {
		view.ExecutionToken = command.ExecutionToken
	}
	if command.Interlock != nil {
		view.Interlock = &contracts.InterlockDecisionView{
			Verdict:    string(command.Interlock.Verdict),
			Verifier:   command.Interlock.Verifier,
			Note:       command.Interlock.Note,
			ReviewedAt: formatTime(command.Interlock.ReviewedAt),
		}
	}
	return view
}

func reviewQueueView(queue domain.ReviewQueue) contracts.ReviewQueueView {
	ordered := queue.Ordered()
	view := contracts.ReviewQueueView{
		GateID:    queue.GateID,
		Entries:   make([]contracts.ReviewQueueEntryView, 0, len(ordered)),
		UpdatedAt: formatTime(queue.UpdatedAt),
		Version:   queue.Version,
	}
	for index, entry := range ordered {
		if index == 0 {
			view.HeadCommandID = entry.CommandID
		}
		view.Entries = append(view.Entries, contracts.ReviewQueueEntryView{
			CommandID:     entry.CommandID,
			Priority:      entry.Priority,
			PriorityBasis: entry.PriorityBasis,
			QueuedAt:      formatTime(entry.QueuedAt),
			Position:      index + 1,
			IsHead:        index == 0,
		})
	}
	return view
}
