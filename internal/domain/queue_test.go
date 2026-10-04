package domain

import (
	"testing"
	"time"
)

func testTime() time.Time {
	return time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
}

func TestReviewQueueFIFOOrderAndHead(t *testing.T) {
	now := testTime()
	queue := NewReviewQueue("gate-1", now)

	for _, id := range []string{"cmd-a", "cmd-b", "cmd-c"} {
		if _, err := queue.Enqueue(id, DefaultPriority, InitialQueueBasis, "dispatcher-lin", now); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}

	head, ok := queue.Head()
	if !ok {
		t.Fatal("expected a queue head")
	}
	if head.CommandID != "cmd-a" {
		t.Fatalf("head: expected cmd-a, got %q", head.CommandID)
	}

	got := orderedIDs(&queue)
	want := []string{"cmd-a", "cmd-b", "cmd-c"}
	if !equalStrings(got, want) {
		t.Fatalf("order: expected %v, got %v", want, got)
	}
}

func TestReviewQueuePriorityOverridesFIFOAndTiesKeepSequence(t *testing.T) {
	now := testTime()
	queue := NewReviewQueue("gate-1", now)

	if _, err := queue.Enqueue("cmd-a", 0, InitialQueueBasis, "dispatcher-lin", now); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Enqueue("cmd-b", 0, InitialQueueBasis, "dispatcher-lin", now); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Enqueue("cmd-c", 0, InitialQueueBasis, "dispatcher-lin", now); err != nil {
		t.Fatal(err)
	}

	entry, err := queue.Reprioritize("cmd-c", 10, "flood risk", "dispatcher-lin", now)
	if err != nil {
		t.Fatalf("reprioritize: %v", err)
	}
	if entry.Priority != 10 || entry.Basis() != "flood risk" {
		t.Fatalf("updated entry mismatch: %+v", entry)
	}
	if got := orderedIDs(&queue); !equalStrings(got, []string{"cmd-c", "cmd-a", "cmd-b"}) {
		t.Fatalf("after promotion: %v", got)
	}

	// Same priority tie keeps the earlier enqueue sequence ahead.
	if _, err := queue.Reprioritize("cmd-b", 10, "same urgency", "dispatcher-lin", now); err != nil {
		t.Fatal(err)
	}
	if got := orderedIDs(&queue); !equalStrings(got, []string{"cmd-b", "cmd-c", "cmd-a"}) {
		t.Fatalf("after tie: %v", got)
	}

	// Negative priority demotes below the default.
	if _, err := queue.Reprioritize("cmd-a", -5, "can wait", "dispatcher-lin", now); err != nil {
		t.Fatal(err)
	}
	if got := orderedIDs(&queue); !equalStrings(got, []string{"cmd-b", "cmd-c", "cmd-a"}) {
		t.Fatalf("after demotion: %v", got)
	}
}

func TestReviewQueueDuplicateEnqueueDoesNotRewriteEntry(t *testing.T) {
	now := testTime()
	queue := NewReviewQueue("gate-1", now)

	first, err := queue.Enqueue("cmd-a", 0, InitialQueueBasis, "dispatcher-lin", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Reprioritize("cmd-a", 8, "urgent", "dispatcher-lin", now); err != nil {
		t.Fatal(err)
	}

	if _, err := queue.Enqueue("cmd-a", 0, InitialQueueBasis, "dispatcher-lin", now); err == nil {
		t.Fatal("expected duplicate enqueue to be rejected")
	} else if ErrorCodeOf(err) != "command_already_queued" {
		t.Fatalf("duplicate enqueue code: %v", err)
	}

	entry, err := queue.Reprioritize("cmd-a", 8, "urgent", "dispatcher-lin", now)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Sequence != first.Sequence {
		t.Fatalf("sequence changed: before=%d after=%d", first.Sequence, entry.Sequence)
	}
	if len(entry.History) != 2 {
		t.Fatalf("history rewritten by duplicate enqueue: %+v", entry.History)
	}
}

func TestReviewQueueIdenticalAdjustmentIsNoOp(t *testing.T) {
	now := testTime()
	queue := NewReviewQueue("gate-1", now)
	if _, err := queue.Enqueue("cmd-a", 0, InitialQueueBasis, "dispatcher-lin", now); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Reprioritize("cmd-a", 5, "basis one", "dispatcher-lin", now); err != nil {
		t.Fatal(err)
	}
	versionAfterChange := queue.Version

	if _, err := queue.Reprioritize("cmd-a", 5, "basis one", "someone-else", now); err != nil {
		t.Fatal(err)
	}
	if queue.Version != versionAfterChange {
		t.Fatalf("identical adjustment bumped version: %d -> %d", versionAfterChange, queue.Version)
	}
	entry, _ := queue.Reprioritize("cmd-a", 5, "basis one", "someone-else", now)
	if got := entry.ChangedBy(); got != "dispatcher-lin" {
		t.Fatalf("no-op adjustment rewrote changed_by: %q", got)
	}
}

func TestReviewQueueAdjustmentValidation(t *testing.T) {
	now := testTime()
	queue := NewReviewQueue("gate-1", now)
	if _, err := queue.Enqueue("cmd-a", 0, InitialQueueBasis, "dispatcher-lin", now); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		priority  int
		basis     string
		changedBy string
		code      string
	}{
		{"priority too high", MaxPriority + 1, "x", "u", "invalid_priority"},
		{"priority too low", MinPriority - 1, "x", "u", "invalid_priority"},
		{"blank basis", 1, "  ", "u", "invalid_priority_basis"},
		{"blank changed_by", 1, "x", " ", "invalid_priority_changed_by"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := queue.Reprioritize("cmd-a", tc.priority, tc.basis, tc.changedBy, now)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if ErrorCodeOf(err) != tc.code {
				t.Fatalf("code: expected %s, got %v", tc.code, err)
			}
		})
	}

	if _, err := queue.Reprioritize("missing", 1, "x", "u", now); err == nil ||
		ErrorCodeOf(err) != "command_not_queued" {
		t.Fatalf("expected command_not_queued, got %v", err)
	}
}

func TestReviewQueueRemoveAndRetainPending(t *testing.T) {
	now := testTime()
	queue := NewReviewQueue("gate-1", now)
	for _, id := range []string{"cmd-a", "cmd-b", "cmd-c"} {
		if _, err := queue.Enqueue(id, 0, InitialQueueBasis, "dispatcher-lin", now); err != nil {
			t.Fatal(err)
		}
	}

	if !queue.Remove("cmd-a", now) {
		t.Fatal("remove returned false for present entry")
	}
	if queue.Remove("cmd-a", now) {
		t.Fatal("remove returned true for absent entry")
	}
	if got := orderedIDs(&queue); !equalStrings(got, []string{"cmd-b", "cmd-c"}) {
		t.Fatalf("after remove: %v", got)
	}

	// Recompute head from real conditions: cmd-c is no longer pending.
	queue.RetainPending(func(id string) bool { return id == "cmd-b" }, now)
	head, ok := queue.Head()
	if !ok || head.CommandID != "cmd-b" {
		t.Fatalf("head after retain: ok=%v head=%q", ok, head.CommandID)
	}

	queue.RetainPending(func(string) bool { return false }, now)
	if _, ok := queue.Head(); ok {
		t.Fatal("empty queue should have no head")
	}
}

func TestReviewQueueEntriesAreIndependentCopies(t *testing.T) {
	now := testTime()
	queue := NewReviewQueue("gate-1", now)
	if _, err := queue.Enqueue("cmd-a", 0, InitialQueueBasis, "dispatcher-lin", now); err != nil {
		t.Fatal(err)
	}
	entries := queue.Entries()
	entries[0].History[0].Basis = "tampered"
	entries[0].CommandID = "tampered"

	again := queue.Entries()
	if again[0].CommandID != "cmd-a" || again[0].Basis() != InitialQueueBasis {
		t.Fatal("queue internal state leaked through Entries copies")
	}
}

func orderedIDs(queue *ReviewQueue) []string {
	entries := queue.Entries()
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.CommandID)
	}
	return ids
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
