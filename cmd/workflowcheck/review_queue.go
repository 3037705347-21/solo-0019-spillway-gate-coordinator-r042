package main

import (
	"fmt"
	"net/http"
)

func runReviewQueue(client *apiClient) error {
	if _, err := fixtureGate(client, "gate-queue", "队列泄洪闸", 6, 0.5); err != nil {
		return err
	}

	c1, err := fixtureCommand(client, "gate-queue", 1.0, "队列命令一", "dispatcher-lin")
	if err != nil {
		return err
	}
	id1, err := stringField(c1, "id")
	if err != nil {
		return err
	}
	c2, err := fixtureCommand(client, "gate-queue", 2.0, "队列命令二", "dispatcher-lin")
	if err != nil {
		return err
	}
	id2, err := stringField(c2, "id")
	if err != nil {
		return err
	}
	c3, err := fixtureCommand(client, "gate-queue", 3.0, "队列命令三", "dispatcher-lin")
	if err != nil {
		return err
	}
	id3, err := stringField(c3, "id")
	if err != nil {
		return err
	}

	// Initially the queue is FIFO by submission order.
	queue, err := getQueue(client, "gate-queue")
	if err != nil {
		return err
	}
	if err := expectQueueOrder(queue, []string{id1, id2, id3}); err != nil {
		return err
	}
	headID, _ := stringField(queue, "head_id")
	if headID != id1 {
		return fmt.Errorf("initial head: expected %q, got %q", id1, headID)
	}
	if err := expectEntry(queue, id1, 0, "initial enqueue; submission FIFO order"); err != nil {
		return err
	}

	// A command that is not the head cannot enter review.
	if err := expectReviewConflict(client, id2); err != nil {
		return fmt.Errorf("non-head clear review: %w", err)
	}
	if err := expectReviewConflict(client, id3); err != nil {
		return fmt.Errorf("non-head blocked review: %w", err)
	}

	// Duplicate enqueue attempts are rejected and cannot move the entry.
	duplicate, err := client.post("/commands/"+id2+"/enqueue", nil)
	if err != nil {
		return err
	}
	if err := requireStatus(duplicate, http.StatusConflict, "reject duplicate enqueue"); err != nil {
		return err
	}
	if code, _ := stringField(duplicate.Body, "code"); code != "command_already_queued" {
		return fmt.Errorf("duplicate enqueue code: expected command_already_queued, got %q", code)
	}
	queue, err = getQueue(client, "gate-queue")
	if err != nil {
		return err
	}
	if err := expectQueueOrder(queue, []string{id1, id2, id3}); err != nil {
		return err
	}

	// A priority adjustment with a recorded basis promotes c3 to head.
	promote, err := client.post("/commands/"+id3+"/priority", map[string]any{
		"priority":   10,
		"basis":      "上游来水急涨，优先预泄",
		"changed_by": "dispatcher-lin",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(promote, http.StatusOK, "promote third command by priority"); err != nil {
		return err
	}
	promotedQueue, err := objectField(promote.Body, "queue")
	if err != nil {
		return err
	}
	if err := expectQueueOrder(promotedQueue, []string{id3, id1, id2}); err != nil {
		return err
	}
	if err := expectEntry(promotedQueue, id3, 10, "上游来水急涨，优先预泄"); err != nil {
		return err
	}

	// The whole history of the priority basis remains readable.
	if err := expectHistory(promotedQueue, id3, []priorityFact{
		{Priority: 0, Basis: "initial enqueue; submission FIFO order"},
		{Priority: 10, Basis: "上游来水急涨，优先预泄"},
	}); err != nil {
		return err
	}

	// Ties keep FIFO order at the same priority level: c2 has an earlier
	// enqueue sequence than c3, so promoting c2 to priority 10 puts it
	// ahead of c3 and makes c2 the head.
	tie, err := client.post("/commands/"+id2+"/priority", map[string]any{
		"priority":   10,
		"basis":      "同等紧急，按先后处理",
		"changed_by": "dispatcher-lin",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(tie, http.StatusOK, "promote second command to tie"); err != nil {
		return err
	}
	tiedQueue, err := objectField(tie.Body, "queue")
	if err != nil {
		return err
	}
	if err := expectQueueOrder(tiedQueue, []string{id2, id3, id1}); err != nil {
		return err
	}

	// Repeating the exact priority and basis is a no-op: version does not
	// move and history keeps a single matching record.
	before, _ := floatField(tiedQueue, "version")
	repeat, err := client.post("/commands/"+id2+"/priority", map[string]any{
		"priority":   10,
		"basis":      "同等紧急，按先后处理",
		"changed_by": "dispatcher-lin",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(repeat, http.StatusOK, "idempotent priority repeat"); err != nil {
		return err
	}
	repeatedQueue, err := objectField(repeat.Body, "queue")
	if err != nil {
		return err
	}
	after, _ := floatField(repeatedQueue, "version")
	if after != before {
		return fmt.Errorf("identical priority adjustment changed version: before=%v after=%v", before, after)
	}
	if err := expectHistory(repeatedQueue, id2, []priorityFact{
		{Priority: 0, Basis: "initial enqueue; submission FIFO order"},
		{Priority: 10, Basis: "同等紧急，按先后处理"},
	}); err != nil {
		return err
	}

	// Invalid adjustment payloads are rejected and rewrite nothing.
	invalid, err := client.post("/commands/"+id1+"/priority", map[string]any{
		"priority":   9999,
		"basis":      "越界优先级",
		"changed_by": "dispatcher-lin",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(invalid, http.StatusBadRequest, "reject out-of-range priority"); err != nil {
		return err
	}
	missingBasis, err := client.post("/commands/"+id1+"/priority", map[string]any{
		"priority":   5,
		"basis":      "   ",
		"changed_by": "dispatcher-lin",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(missingBasis, http.StatusBadRequest, "reject missing priority basis"); err != nil {
		return err
	}

	// Only the current head can be reviewed: clear c2 and execute it so
	// the next item advances from real conditions.
	approved2, err := fixtureClearReview(client, id2, "safety-wu")
	if err != nil {
		return err
	}
	if status, _ := stringField(approved2, "status"); status != "approved" {
		return fmt.Errorf("head review status: expected approved, got %q", status)
	}
	queue, err = getQueue(client, "gate-queue")
	if err != nil {
		return err
	}
	if err := expectQueueOrder(queue, []string{id3, id1}); err != nil {
		return err
	}

	token2, err := stringField(approved2, "execution_token")
	if err != nil {
		return err
	}
	executed2, err := client.post("/commands/"+id2+"/execute", map[string]any{
		"operator":       "operator-chen",
		"token":          token2,
		"simulate_fault": false,
	})
	if err != nil {
		return err
	}
	if err := requireStatus(executed2, http.StatusOK, "execute reviewed head"); err != nil {
		return err
	}

	// Priority adjustment or re-enqueue cannot reach a command already in
	// another state.
	lateAdjust, err := client.post("/commands/"+id2+"/priority", map[string]any{
		"priority":   50,
		"basis":      "事后调整",
		"changed_by": "dispatcher-lin",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(lateAdjust, http.StatusConflict, "reject adjustment of completed command"); err != nil {
		return err
	}
	lateEnqueue, err := client.post("/commands/"+id2+"/enqueue", nil)
	if err != nil {
		return err
	}
	if err := requireStatus(lateEnqueue, http.StatusConflict, "reject re-enqueue of completed command"); err != nil {
		return err
	}

	// After a blocked review of the new head, c1 becomes the current head
	// purely from the remaining real queue.
	blocked3, err := client.post("/commands/"+id3+"/interlock", map[string]any{
		"verdict":  "blocked",
		"verifier": "safety-wu",
		"note":     "下游施工，暂不放水",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(blocked3, http.StatusOK, "block current head"); err != nil {
		return err
	}
	blockedCommand, err := objectField(blocked3.Body, "command")
	if err != nil {
		return err
	}
	if status, _ := stringField(blockedCommand, "status"); status != "rejected" {
		return fmt.Errorf("blocked review status: expected rejected, got %q", status)
	}
	queue, err = getQueue(client, "gate-queue")
	if err != nil {
		return err
	}
	if err := expectQueueOrder(queue, []string{id1}); err != nil {
		return err
	}
	if head, _ := stringField(queue, "head_id"); head != id1 {
		return fmt.Errorf("final head: expected %q, got %q", id1, head)
	}

	// A reviewed/rejected command cannot enter review again, and the
	// committed facts are still readable.
	if err := expectReviewConflict(client, id3); err != nil {
		return fmt.Errorf("re-review of rejected command: %w", err)
	}

	fmt.Println("review-queue: queue order, priority basis, head gating and completion advancement verified")
	return nil
}

type priorityFact struct {
	Priority int
	Basis    string
}

func getQueue(client *apiClient, gateID string) (map[string]any, error) {
	result, err := client.get("/gates/" + gateID + "/review-queue")
	if err != nil {
		return nil, err
	}
	if err := requireStatus(result, http.StatusOK, "read review queue"); err != nil {
		return nil, err
	}
	return objectField(result.Body, "queue")
}

func queueEntries(queue map[string]any) ([]any, error) {
	value, exists := queue["entries"]
	if !exists {
		return nil, fmt.Errorf("queue is missing entries field")
	}
	entries, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("queue entries field is not an array")
	}
	return entries, nil
}

func expectQueueOrder(queue map[string]any, expected []string) error {
	entries, err := queueEntries(queue)
	if err != nil {
		return err
	}
	if len(entries) != len(expected) {
		return fmt.Errorf("queue length: expected %d, got %d", len(expected), len(entries))
	}
	for i, want := range expected {
		entry, ok := entries[i].(map[string]any)
		if !ok {
			return fmt.Errorf("queue entry %d is not an object", i)
		}
		got, err := stringField(entry, "command_id")
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("queue position %d: expected command %q, got %q", i+1, want, got)
		}
		position, err := floatField(entry, "position")
		if err != nil {
			return err
		}
		if int(position) != i+1 {
			return fmt.Errorf("entry %q position: expected %d, got %v", want, i+1, position)
		}
	}
	length, err := floatField(queue, "length")
	if err != nil {
		return err
	}
	if int(length) != len(expected) {
		return fmt.Errorf("queue length field: expected %d, got %v", len(expected), length)
	}
	return nil
}

func findEntry(queue map[string]any, commandID string) (map[string]any, error) {
	entries, err := queueEntries(queue)
	if err != nil {
		return nil, err
	}
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("queue entry is not an object")
		}
		id, err := stringField(entry, "command_id")
		if err != nil {
			return nil, err
		}
		if id == commandID {
			return entry, nil
		}
	}
	return nil, fmt.Errorf("command %q is not in the queue", commandID)
}

func expectEntry(queue map[string]any, commandID string, priority int, basis string) error {
	entry, err := findEntry(queue, commandID)
	if err != nil {
		return err
	}
	gotPriority, err := floatField(entry, "priority")
	if err != nil {
		return err
	}
	if int(gotPriority) != priority {
		return fmt.Errorf("entry %q priority: expected %d, got %v", commandID, priority, gotPriority)
	}
	gotBasis, err := stringField(entry, "basis")
	if err != nil {
		return err
	}
	if gotBasis != basis {
		return fmt.Errorf("entry %q basis: expected %q, got %q", commandID, basis, gotBasis)
	}
	return nil
}

func expectHistory(queue map[string]any, commandID string, facts []priorityFact) error {
	entry, err := findEntry(queue, commandID)
	if err != nil {
		return err
	}
	value, exists := entry["history"]
	if !exists {
		return fmt.Errorf("entry %q missing history", commandID)
	}
	history, ok := value.([]any)
	if !ok {
		return fmt.Errorf("entry %q history is not an array", commandID)
	}
	if len(history) != len(facts) {
		return fmt.Errorf("entry %q history length: expected %d, got %d", commandID, len(facts), len(history))
	}
	for i, fact := range facts {
		record, ok := history[i].(map[string]any)
		if !ok {
			return fmt.Errorf("history record %d is not an object", i)
		}
		priority, err := floatField(record, "priority")
		if err != nil {
			return err
		}
		if int(priority) != fact.Priority {
			return fmt.Errorf("history record %d priority: expected %d, got %v", i, fact.Priority, priority)
		}
		basis, err := stringField(record, "basis")
		if err != nil {
			return err
		}
		if basis != fact.Basis {
			return fmt.Errorf("history record %d basis: expected %q, got %q", i, fact.Basis, basis)
		}
	}
	return nil
}

func expectReviewConflict(client *apiClient, commandID string) error {
	result, err := client.post("/commands/"+commandID+"/interlock", map[string]any{
		"verdict":  "clear",
		"verifier": "safety-wu",
		"note":     "越权复核验证",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(result, http.StatusConflict, "reject review outside current head"); err != nil {
		return err
	}
	return nil
}
