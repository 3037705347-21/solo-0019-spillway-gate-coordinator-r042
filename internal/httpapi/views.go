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
	entries := queue.Entries()
	view := contracts.ReviewQueueView{
		GateID:    queue.GateID,
		Entries:   make([]contracts.ReviewQueueEntryView, 0, len(entries)),
		Length:    len(entries),
		UpdatedAt: formatTime(queue.UpdatedAt),
		Version:   queue.Version,
	}
	for position, entry := range entries {
		history := make([]contracts.PriorityChangeView, 0, len(entry.History))
		for _, change := range entry.History {
			history = append(history, contracts.PriorityChangeView{
				Priority:  change.Priority,
				Basis:     change.Basis,
				ChangedBy: change.ChangedBy,
				ChangedAt: formatTime(change.ChangedAt),
			})
		}
		item := contracts.ReviewQueueEntryView{
			CommandID:  entry.CommandID,
			Position:   position + 1,
			Priority:   entry.Priority,
			Basis:      entry.Basis(),
			ChangedBy:  entry.ChangedBy(),
			EnqueuedAt: formatTime(entry.EnqueuedAt),
			History:    history,
		}
		view.Entries = append(view.Entries, item)
	}
	if head, ok := queue.Head(); ok {
		view.HeadID = head.CommandID
	}
	return view
}
