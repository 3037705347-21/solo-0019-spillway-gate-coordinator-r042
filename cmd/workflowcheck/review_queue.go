package main

import (
	"fmt"
	"net/http"
	"sync"
)

func runReviewQueuePriority(client *apiClient) error {
	if _, err := fixtureGate(client, "gate-queue", "队列验证闸", 6, 0.5); err != nil {
		return err
	}

	cmdA, err := fixtureCommand(client, "gate-queue", 1.0, "第一轮调整", "dispatcher-lin")
	if err != nil {
		return err
	}
	cmdB, err := fixtureCommand(client, "gate-queue", 1.5, "第二轮调整", "dispatcher-lin")
	if err != nil {
		return err
	}
	cmdC, err := fixtureCommand(client, "gate-queue", 2.0, "洪峰预备", "dispatcher-lin")
	if err != nil {
		return err
	}
	idA, err := stringField(cmdA, "id")
	if err != nil {
		return err
	}
	idB, err := stringField(cmdB, "id")
	if err != nil {
		return err
	}
	idC, err := stringField(cmdC, "id")
	if err != nil {
		return err
	}

	// 队列可读：相同优先级按到达次序排队，A 为队首。
	if err := expectQueueOrder(client, "gate-queue", []string{idA, idB, idC}, idA); err != nil {
		return err
	}

	// 非队首不能进入复核，且不得改写任何已发生的处理事实。
	blocked, err := client.post("/commands/"+idB+"/interlock", map[string]any{
		"verdict":  "clear",
		"verifier": "safety-wu",
		"note":     "插队复核尝试",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(blocked, http.StatusConflict, "reject non-head review"); err != nil {
		return err
	}
	if err := expectErrorCode(blocked, "command_not_queue_head"); err != nil {
		return err
	}
	if err := expectCommandStatus(client, idB, "pending"); err != nil {
		return err
	}
	if err := expectQueueOrder(client, "gate-queue", []string{idA, idB, idC}, idA); err != nil {
		return err
	}

	// 调整优先级：C 提级后成为队首，依据可读。
	reprioritized, err := client.post("/commands/"+idC+"/priority", map[string]any{
		"priority": 10,
		"basis":    "上游洪峰临近",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(reprioritized, http.StatusOK, "reprioritize queued command"); err != nil {
		return err
	}
	reprioritizedQueue, err := objectField(reprioritized.Body, "queue")
	if err != nil {
		return err
	}
	headID, err := stringField(reprioritizedQueue, "head_command_id")
	if err != nil {
		return err
	}
	if headID != idC {
		return fmt.Errorf("head after reprioritize: expected %q, got %q", idC, headID)
	}
	if err := expectQueueOrder(client, "gate-queue", []string{idC, idA, idB}, idC); err != nil {
		return err
	}
	if err := expectQueueEntryBasis(client, "gate-queue", idC, 10, "上游洪峰临近"); err != nil {
		return err
	}
	if err := expectCommandPriority(client, idC, 10, "上游洪峰临近"); err != nil {
		return err
	}

	// 调整优先级必须说明依据。
	emptyBasis, err := client.post("/commands/"+idC+"/priority", map[string]any{
		"priority": 5,
		"basis":    "   ",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(emptyBasis, http.StatusBadRequest, "reject priority change without basis"); err != nil {
		return err
	}

	// A 已不再是队首，复核 A 仍然被拒绝。
	notHead, err := client.post("/commands/"+idA+"/interlock", map[string]any{
		"verdict":  "clear",
		"verifier": "safety-wu",
		"note":     "旧队首复核尝试",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(notHead, http.StatusConflict, "reject stale head review"); err != nil {
		return err
	}

	// 队首 C 复核 blocked 后出队，下一项按当时真实条件（A、B 同优先级，A 先到）成为队首。
	rejectedC, err := client.post("/commands/"+idC+"/interlock", map[string]any{
		"verdict":  "blocked",
		"verifier": "safety-wu",
		"note":     "下游泄洪通道检修",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(rejectedC, http.StatusOK, "review head command blocked"); err != nil {
		return err
	}
	if err := expectQueueOrder(client, "gate-queue", []string{idA, idB}, idA); err != nil {
		return err
	}

	// 已 rejected 的命令不能再调整优先级，处理事实不被改写。
	rejectedReprioritize, err := client.post("/commands/"+idC+"/priority", map[string]any{
		"priority": 20,
		"basis":    "试图改写已拒绝命令",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(rejectedReprioritize, http.StatusConflict, "reject reprioritize of rejected command"); err != nil {
		return err
	}
	if err := expectErrorCode(rejectedReprioritize, "command_not_pending"); err != nil {
		return err
	}
	if err := expectCommandStatus(client, idC, "rejected"); err != nil {
		return err
	}
	if err := expectCommandPriority(client, idC, 10, "上游洪峰临近"); err != nil {
		return err
	}

	// B 提级后超过 A 成为队首：队首按当前真实条件动态确定。
	if _, err := fixtureReprioritize(client, idB, 5, "机组检修窗口"); err != nil {
		return err
	}
	if err := expectQueueOrder(client, "gate-queue", []string{idB, idA}, idB); err != nil {
		return err
	}
	staleHead, err := client.post("/commands/"+idA+"/interlock", map[string]any{
		"verdict":  "clear",
		"verifier": "safety-wu",
		"note":     "再次尝试旧队首",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(staleHead, http.StatusConflict, "reject review of demoted command"); err != nil {
		return err
	}

	// 队首 B 复核 clear：签发令牌并占用闸门，B 出队后 A 回到队首。
	approvedB, err := fixtureClearReview(client, idB, "safety-wu")
	if err != nil {
		return err
	}
	tokenB, err := stringField(approvedB, "execution_token")
	if err != nil {
		return err
	}
	if err := expectQueueOrder(client, "gate-queue", []string{idA}, idA); err != nil {
		return err
	}

	// 已 approved 的命令不能再调整优先级。
	approvedReprioritize, err := client.post("/commands/"+idB+"/priority", map[string]any{
		"priority": 1,
		"basis":    "试图改写已批准命令",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(approvedReprioritize, http.StatusConflict, "reject reprioritize of approved command"); err != nil {
		return err
	}

	// A 是队首可以进入复核，但闸门已被 B 占用，clear 仍然冲突且不改变闸门。
	occupied, err := client.post("/commands/"+idA+"/interlock", map[string]any{
		"verdict":  "clear",
		"verifier": "safety-wu",
		"note":     "闸门占用冲突验证",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(occupied, http.StatusConflict, "reject clear review on reserved gate"); err != nil {
		return err
	}
	if err := expectErrorCode(occupied, "gate_not_idle"); err != nil {
		return err
	}
	if err := expectCommandStatus(client, idA, "pending"); err != nil {
		return err
	}

	// A 以 blocked 完成复核并出队，队列清空。
	drained, err := client.post("/commands/"+idA+"/interlock", map[string]any{
		"verdict":  "blocked",
		"verifier": "safety-wu",
		"note":     "计划取消",
	})
	if err != nil {
		return err
	}
	if err := requireStatus(drained, http.StatusOK, "drain last queued command"); err != nil {
		return err
	}
	if err := expectQueueOrder(client, "gate-queue", []string{}, ""); err != nil {
		return err
	}

	// B 的执行生命周期不受队列影响，照常完成。
	executed, err := client.post("/commands/"+idB+"/execute", map[string]any{
		"operator":       "operator-chen",
		"token":          tokenB,
		"simulate_fault": false,
	})
	if err != nil {
		return err
	}
	if err := requireStatus(executed, http.StatusOK, "execute approved command"); err != nil {
		return err
	}

	// 并发复核同一个队首：只有一个赢家，另一个收到冲突，事实只被写入一次。
	if err := expectConcurrentReviewSingleWinner(client); err != nil {
		return err
	}

	fmt.Println("review-queue-priority: queue order, head gating, reprioritize, and concurrent review verified")
	return nil
}

func expectConcurrentReviewSingleWinner(client *apiClient) error {
	if _, err := fixtureGate(client, "gate-queue-race", "并发复核闸", 4, 0.25); err != nil {
		return err
	}
	command, err := fixtureCommand(client, "gate-queue-race", 1.25, "并发复核验证", "dispatcher-lin")
	if err != nil {
		return err
	}
	commandID, err := stringField(command, "id")
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := client.post("/commands/"+commandID+"/interlock", map[string]any{
				"verdict":  "clear",
				"verifier": "safety-wu",
				"note":     "并发复核竞争",
			})
			if err != nil {
				statuses <- -1
				return
			}
			statuses <- result.Status
		}()
	}
	wg.Wait()
	close(statuses)

	okCount, conflictCount := 0, 0
	for status := range statuses {
		switch status {
		case http.StatusOK:
			okCount++
		case http.StatusConflict:
			conflictCount++
		default:
			return fmt.Errorf("concurrent review: unexpected status %d", status)
		}
	}
	if okCount != 1 || conflictCount != 1 {
		return fmt.Errorf("concurrent review: expected one 200 and one 409, got %d ok and %d conflict", okCount, conflictCount)
	}
	if err := expectCommandStatus(client, commandID, "approved"); err != nil {
		return err
	}
	return expectQueueOrder(client, "gate-queue-race", []string{}, "")
}

func fixtureReprioritize(
	client *apiClient,
	commandID string,
	priority int64,
	basis string,
) (map[string]any, error) {
	result, err := client.post("/commands/"+commandID+"/priority", map[string]any{
		"priority": priority,
		"basis":    basis,
	})
	if err != nil {
		return nil, err
	}
	if err := requireStatus(result, http.StatusOK, "reprioritize fixture command"); err != nil {
		return nil, err
	}
	updated, err := objectField(result.Body, "command")
	if err != nil {
		return nil, fmt.Errorf("reprioritize fixture command response: %w", err)
	}
	return updated, nil
}

func fetchQueue(client *apiClient, gateID string) (map[string]any, error) {
	result, err := client.get("/gates/" + gateID + "/queue")
	if err != nil {
		return nil, err
	}
	if err := requireStatus(result, http.StatusOK, "read review queue"); err != nil {
		return nil, err
	}
	queue, err := objectField(result.Body, "queue")
	if err != nil {
		return nil, fmt.Errorf("read review queue response: %w", err)
	}
	return queue, nil
}

func queueCommandIDs(queue map[string]any) ([]string, error) {
	entries, err := arrayField(queue, "entries")
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("queue entry is not an object")
		}
		id, err := stringField(entry, "command_id")
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func expectQueueOrder(client *apiClient, gateID string, expected []string, expectedHead string) error {
	queue, err := fetchQueue(client, gateID)
	if err != nil {
		return err
	}
	ids, err := queueCommandIDs(queue)
	if err != nil {
		return err
	}
	if len(ids) != len(expected) {
		return fmt.Errorf("queue length: expected %d entries %v, got %v", len(expected), expected, ids)
	}
	seen := map[string]bool{}
	for i, id := range expected {
		if ids[i] != id {
			return fmt.Errorf("queue order: expected %v, got %v", expected, ids)
		}
		if seen[id] {
			return fmt.Errorf("queue contains duplicate entry %q", id)
		}
		seen[id] = true
	}
	if expectedHead == "" {
		if _, exists := queue["head_command_id"]; exists {
			return fmt.Errorf("empty queue must not report a head, got %v", queue["head_command_id"])
		}
		return nil
	}
	headID, err := stringField(queue, "head_command_id")
	if err != nil {
		return err
	}
	if headID != expectedHead {
		return fmt.Errorf("queue head: expected %q, got %q", expectedHead, headID)
	}
	return nil
}

func expectQueueEntryBasis(client *apiClient, gateID string, commandID string, priority float64, basis string) error {
	queue, err := fetchQueue(client, gateID)
	if err != nil {
		return err
	}
	entries, err := arrayField(queue, "entries")
	if err != nil {
		return err
	}
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("queue entry is not an object")
		}
		id, err := stringField(entry, "command_id")
		if err != nil {
			return err
		}
		if id != commandID {
			continue
		}
		entryPriority, err := floatField(entry, "priority")
		if err != nil {
			return err
		}
		entryBasis, err := stringField(entry, "priority_basis")
		if err != nil {
			return err
		}
		if entryPriority != priority || entryBasis != basis {
			return fmt.Errorf(
				"queue entry %q: expected priority %v basis %q, got %v %q",
				commandID, priority, basis, entryPriority, entryBasis,
			)
		}
		return nil
	}
	return fmt.Errorf("queue entry %q not found", commandID)
}

func expectCommandStatus(client *apiClient, commandID string, expected string) error {
	result, err := client.get("/commands/" + commandID)
	if err != nil {
		return err
	}
	if err := requireStatus(result, http.StatusOK, "read command"); err != nil {
		return err
	}
	command, err := objectField(result.Body, "command")
	if err != nil {
		return err
	}
	status, err := stringField(command, "status")
	if err != nil {
		return err
	}
	if status != expected {
		return fmt.Errorf("command %s status: expected %q, got %q", commandID, expected, status)
	}
	return nil
}

func expectCommandPriority(client *apiClient, commandID string, priority float64, basis string) error {
	result, err := client.get("/commands/" + commandID)
	if err != nil {
		return err
	}
	if err := requireStatus(result, http.StatusOK, "read command"); err != nil {
		return err
	}
	command, err := objectField(result.Body, "command")
	if err != nil {
		return err
	}
	actualPriority, err := floatField(command, "priority")
	if err != nil {
		return err
	}
	actualBasis, err := stringField(command, "priority_basis")
	if err != nil {
		return err
	}
	if actualPriority != priority || actualBasis != basis {
		return fmt.Errorf(
			"command %s priority: expected %v %q, got %v %q",
			commandID, priority, basis, actualPriority, actualBasis,
		)
	}
	return nil
}

func expectErrorCode(result apiResult, expected string) error {
	code, err := stringField(result.Body, "code")
	if err != nil {
		return err
	}
	if code != expected {
		return fmt.Errorf("error code: expected %q, got %q", expected, code)
	}
	return nil
}
