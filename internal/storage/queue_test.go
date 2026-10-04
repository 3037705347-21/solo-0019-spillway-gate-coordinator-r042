package storage

import (
	"context"
	"sync"
	"testing"
	"time"

	"example.com/spillway-gate-coordinator/internal/domain"
)

func newSeededRepository(t *testing.T, gateID string, targetApertures ...float64) (*MemoryRepository, []domain.GateCommand) {
	t.Helper()
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	repository := NewMemoryRepository()
	ctx := context.Background()

	gate, err := domain.NewGate(gateID, "测试闸门", 10, 1.0, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateGate(ctx, gate); err != nil {
		t.Fatal(err)
	}

	commands := make([]domain.GateCommand, 0, len(targetApertures))
	for i, target := range targetApertures {
		// Keep the stored aperture in sync so every starting snapshot is
		// valid against the gate.
		gate.Aperture = 1.0 + float64(i)
		repository.gates[gateID] = gate
		command, err := domain.NewGateCommand(
			"cmd-"+gateID+"-"+string(rune('a'+i)),
			gateID,
			target,
			gate.Aperture,
			gate.MaxAperture,
			"排队测试",
			"dispatcher-lin",
			now.Add(time.Duration(i)*time.Second),
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.CreateCommand(ctx, command); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, command)
	}
	return repository, commands
}

func clearDecision(now time.Time) domain.InterlockDecision {
	decision, err := domain.NewInterlockDecision("clear", "safety-wu", "条件满足", now)
	if err != nil {
		panic(err)
	}
	return decision
}

func blockedDecision(now time.Time) domain.InterlockDecision {
	decision, err := domain.NewInterlockDecision("blocked", "safety-wu", "条件不满足", now)
	if err != nil {
		panic(err)
	}
	return decision
}

func TestCreateCommandAutoEnqueuesFIFO(t *testing.T) {
	repository, commands := newSeededRepository(t, "gate-auto", 2.0, 3.0)

	queue, err := repository.ReviewQueue(context.Background(), "gate-auto")
	if err != nil {
		t.Fatal(err)
	}
	head, ok := queue.Head()
	if !ok || head.CommandID != commands[0].ID {
		t.Fatalf("head: expected %q, got %q ok=%v", commands[0].ID, head.CommandID, ok)
	}
	if len(queue.Entries()) != 2 {
		t.Fatalf("queue length: expected 2, got %d", len(queue.Entries()))
	}
	if head.Basis() != domain.InitialQueueBasis {
		t.Fatalf("initial basis: %q", head.Basis())
	}
}

func TestReviewOnlyCurrentHeadSucceeds(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	repository, commands := newSeededRepository(t, "gate-head", 2.0, 3.0, 4.0)

	// The second and third entries cannot jump the queue under either
	// verdict.
	for _, id := range []string{commands[1].ID, commands[2].ID} {
		if _, _, err := repository.ReviewCommand(ctx, id, clearDecision(now), "tok", now); err == nil ||
			domain.ErrorCodeOf(err) != "command_not_queue_head" {
			t.Fatalf("non-head review: %v", err)
		}
	}

	// Blocked review of the head advances the queue without reserving.
	if _, gate, err := repository.ReviewCommand(ctx, commands[0].ID, blockedDecision(now), "", now); err != nil {
		t.Fatalf("block head: %v", err)
	} else if gate.ActiveCommandID != "" || gate.Status != domain.GateIdle {
		t.Fatalf("blocked review touched the gate: %+v", gate)
	}

	queue, _ := repository.ReviewQueue(ctx, "gate-head")
	head, ok := queue.Head()
	if !ok || head.CommandID != commands[1].ID {
		t.Fatalf("next head: got %q ok=%v", head.CommandID, ok)
	}

	// Clear review of the new head reserves the gate and removes it.
	reviewed, gate, err := repository.ReviewCommand(ctx, commands[1].ID, clearDecision(now), "tok-1", now)
	if err != nil {
		t.Fatalf("clear new head: %v", err)
	}
	if reviewed.Status != domain.CommandApproved || gate.ActiveCommandID != commands[1].ID {
		t.Fatalf("clear review facts: command=%s gate=%s", reviewed.Status, gate.ActiveCommandID)
	}
	queue, _ = repository.ReviewQueue(ctx, "gate-head")
	if queue.Contains(commands[1].ID) {
		t.Fatal("reviewed head was not removed from the queue")
	}
	head, _ = queue.Head()
	if head.CommandID != commands[2].ID {
		t.Fatalf("remaining head: %q", head.CommandID)
	}
}

func TestConcurrentClearReviewsHaveSingleWinner(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	repository, commands := newSeededRepository(t, "gate-race", 2.0, 3.0)
	headID := commands[0].ID

	const goroutines = 64
	var wg sync.WaitGroup
	results := make(chan error, goroutines)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _, err := repository.ReviewCommand(ctx, headID, clearDecision(now), "tok", now)
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case domain.ErrorCodeOf(err) == "command_not_pending":
			conflicts++
		default:
			t.Fatalf("unexpected review result: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one winning review, got %d (conflicts=%d)", successes, conflicts)
	}
	if conflicts != goroutines-1 {
		t.Fatalf("expected %d conflicts, got %d", goroutines-1, conflicts)
	}

	gate, _ := repository.Gate(ctx, "gate-race")
	if gate.Status != domain.GateReserved || gate.ActiveCommandID != headID {
		t.Fatalf("gate facts after race: status=%s active=%q", gate.Status, gate.ActiveCommandID)
	}
	stored, _ := repository.Command(ctx, headID)
	if stored.Status != domain.CommandApproved {
		t.Fatalf("head status after race: %s", stored.Status)
	}
}

func TestNonHeadCanNeverReserveUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	repository, commands := newSeededRepository(t, "gate-order", 2.0, 3.0)

	const goroutines = 48
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(goroutines * 2)
	// Half hit the head, half race the second entry.
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			_, _, _ = repository.ReviewCommand(ctx, commands[0].ID, clearDecision(now), "tok", now)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, _, err := repository.ReviewCommand(ctx, commands[1].ID, clearDecision(now), "tok", now)
			// Before the head commits it is a queue violation; once the
			// head completes the second entry legitimately heads the
			// queue but must still lose the gate reservation while the
			// gate is busy. Both outcomes leave the facts untouched.
			if err != nil {
				code := domain.ErrorCodeOf(err)
				if code != "command_not_queue_head" && code != "gate_not_idle" {
					t.Errorf("second entry review error: %v", err)
				}
			}
		}()
	}
	close(start)
	wg.Wait()

	second, _ := repository.Command(ctx, commands[1].ID)
	if second.Status != domain.CommandPending {
		t.Fatalf("non-head command changed state: %s", second.Status)
	}
	gate, _ := repository.Gate(ctx, "gate-order")
	if gate.ActiveCommandID != commands[0].ID {
		t.Fatalf("gate reserved by wrong command: %q", gate.ActiveCommandID)
	}
}

func TestReprioritizeAndEnqueueCannotRewriteCommittedFacts(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	repository, commands := newSeededRepository(t, "gate-facts", 2.0)
	id := commands[0].ID

	if _, _, err := repository.ReviewCommand(ctx, id, blockedDecision(now), "", now); err != nil {
		t.Fatal(err)
	}

	if _, err := repository.ReprioritizeCommand(ctx, id, 99, "事后依据", "dispatcher-lin", now); err == nil ||
		domain.ErrorCodeOf(err) != "command_not_pending" {
		t.Fatalf("adjustment after rejection: %v", err)
	}
	if _, err := repository.EnqueueCommand(ctx, id); err == nil ||
		domain.ErrorCodeOf(err) != "command_not_pending" {
		t.Fatalf("re-enqueue after rejection: %v", err)
	}

	queue, _ := repository.ReviewQueue(ctx, "gate-facts")
	if len(queue.Entries()) != 0 {
		t.Fatalf("rejected command stayed in queue: %d", len(queue.Entries()))
	}
	stored, _ := repository.Command(ctx, id)
	if stored.Status != domain.CommandRejected || stored.Interlock == nil ||
		stored.Interlock.Note != "条件不满足" {
		t.Fatalf("committed review facts changed: %+v", stored)
	}
}
