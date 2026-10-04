package storage

import (
	"context"
	"sync"
	"time"

	"example.com/spillway-gate-coordinator/internal/domain"
)

type MemoryRepository struct {
	mu       sync.RWMutex
	gates    map[string]domain.Gate
	commands map[string]domain.GateCommand
	queues   map[string]domain.ReviewQueue
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		gates:    make(map[string]domain.Gate),
		commands: make(map[string]domain.GateCommand),
		queues:   make(map[string]domain.ReviewQueue),
	}
}

func (m *MemoryRepository) CreateGate(_ context.Context, gate domain.Gate) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.gates[gate.ID]; exists {
		return domain.Conflict("duplicate_gate_id", "gate id already exists")
	}
	m.gates[gate.ID] = gate
	m.queues[gate.ID] = domain.NewReviewQueue(gate.ID, gate.UpdatedAt)
	return nil
}

func (m *MemoryRepository) Gate(_ context.Context, id string) (domain.Gate, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	gate, exists := m.gates[id]
	if !exists {
		return domain.Gate{}, domain.NotFound("gate_not_found", "gate id was not found")
	}
	return gate, nil
}

func (m *MemoryRepository) ReviewQueue(_ context.Context, gateID string) (domain.ReviewQueue, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if _, exists := m.gates[gateID]; !exists {
		return domain.ReviewQueue{}, domain.NotFound("gate_not_found", "gate id was not found")
	}
	queue, exists := m.queues[gateID]
	if !exists {
		return domain.ReviewQueue{}, domain.NotFound("review_queue_not_found", "review queue was not found")
	}
	return cloneQueue(queue), nil
}

func (m *MemoryRepository) CreateCommand(_ context.Context, command domain.GateCommand) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	gate, exists := m.gates[command.GateID]
	if !exists {
		return domain.NotFound("gate_not_found", "gate id was not found")
	}
	if gate.Status == domain.GateFaulted {
		return domain.Conflict("gate_faulted", "faulted gates cannot receive new commands")
	}
	if command.StartingAperture != gate.Aperture {
		return domain.Conflict("gate_aperture_changed", "gate aperture changed before the command was stored")
	}
	if command.TargetAperture > gate.MaxAperture {
		return domain.Invalid("invalid_target_aperture", "target aperture is outside the gate limit")
	}
	if _, exists := m.commands[command.ID]; exists {
		return domain.Conflict("duplicate_command_id", "command id already exists")
	}

	queue, queued := m.queues[command.GateID]
	if !queued {
		queue = domain.NewReviewQueue(command.GateID, command.CreatedAt)
	}
	nextQueue := cloneQueue(queue)
	if err := nextQueue.Enqueue(command.ID, command.Priority, command.PriorityBasis, command.CreatedAt); err != nil {
		return err
	}

	m.commands[command.ID] = command
	m.queues[command.GateID] = nextQueue
	return nil
}

func (m *MemoryRepository) Command(_ context.Context, id string) (domain.GateCommand, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	command, exists := m.commands[id]
	if !exists {
		return domain.GateCommand{}, domain.NotFound("command_not_found", "command id was not found")
	}
	return cloneCommand(command), nil
}

func (m *MemoryRepository) ReprioritizeCommand(
	_ context.Context,
	commandID string,
	priority int64,
	basis string,
	now time.Time,
) (domain.GateCommand, domain.ReviewQueue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	command, exists := m.commands[commandID]
	if !exists {
		return domain.GateCommand{}, domain.ReviewQueue{}, domain.NotFound("command_not_found", "command id was not found")
	}

	nextCommand := cloneCommand(command)
	if err := nextCommand.Reprioritize(priority, basis, now); err != nil {
		return domain.GateCommand{}, domain.ReviewQueue{}, err
	}

	queue, queued := m.queues[command.GateID]
	if !queued {
		return domain.GateCommand{}, domain.ReviewQueue{}, domain.Conflict("command_not_queued", "command is not waiting in the review queue")
	}
	nextQueue := cloneQueue(queue)
	if err := nextQueue.Reprioritize(commandID, nextCommand.Priority, nextCommand.PriorityBasis, now); err != nil {
		return domain.GateCommand{}, domain.ReviewQueue{}, err
	}

	m.commands[nextCommand.ID] = nextCommand
	m.queues[command.GateID] = nextQueue
	return cloneCommand(nextCommand), cloneQueue(nextQueue), nil
}

func (m *MemoryRepository) ReviewCommand(
	_ context.Context,
	commandID string,
	decision domain.InterlockDecision,
	token string,
	now time.Time,
) (domain.GateCommand, domain.Gate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	command, exists := m.commands[commandID]
	if !exists {
		return domain.GateCommand{}, domain.Gate{}, domain.NotFound("command_not_found", "command id was not found")
	}
	gate, exists := m.gates[command.GateID]
	if !exists {
		return domain.GateCommand{}, domain.Gate{}, domain.NotFound("gate_not_found", "gate id was not found")
	}

	if command.Status == domain.CommandPending {
		queue, queued := m.queues[command.GateID]
		if !queued || !queue.Contains(commandID) {
			return domain.GateCommand{}, domain.Gate{}, domain.Conflict("command_not_queued", "command is not waiting in the review queue")
		}
		head, ok := queue.Head()
		if !ok || head.CommandID != commandID {
			return domain.GateCommand{}, domain.Gate{}, domain.Conflict("command_not_queue_head", "only the head of the review queue can be reviewed")
		}
	}

	nextCommand := cloneCommand(command)
	if err := nextCommand.Review(decision, token, now); err != nil {
		return domain.GateCommand{}, domain.Gate{}, err
	}

	nextQueue := cloneQueue(m.queues[command.GateID])
	if err := nextQueue.Dequeue(commandID, now); err != nil {
		return domain.GateCommand{}, domain.Gate{}, err
	}

	nextGate := gate
	if decision.IsClear() {
		if err := nextGate.Reserve(nextCommand.ID, now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
	}

	m.commands[nextCommand.ID] = nextCommand
	m.gates[nextGate.ID] = nextGate
	m.queues[command.GateID] = nextQueue
	return cloneCommand(nextCommand), nextGate, nil
}

func (m *MemoryRepository) ExecuteCommand(
	_ context.Context,
	commandID string,
	token string,
	operator string,
	simulateFault bool,
	now time.Time,
) (domain.GateCommand, domain.Gate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	command, exists := m.commands[commandID]
	if !exists {
		return domain.GateCommand{}, domain.Gate{}, domain.NotFound("command_not_found", "command id was not found")
	}
	gate, exists := m.gates[command.GateID]
	if !exists {
		return domain.GateCommand{}, domain.Gate{}, domain.NotFound("gate_not_found", "gate id was not found")
	}

	nextCommand := cloneCommand(command)
	if err := nextCommand.BeginExecution(token, operator, now); err != nil {
		return domain.GateCommand{}, domain.Gate{}, err
	}

	nextGate := gate
	if err := nextGate.StartMovement(nextCommand.ID, now); err != nil {
		return domain.GateCommand{}, domain.Gate{}, err
	}

	if simulateFault {
		if err := nextGate.Settle(nextCommand.TargetAperture, true, now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
		if err := nextCommand.Abort("simulated_gate_fault", now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
	} else {
		if err := nextGate.Settle(nextCommand.TargetAperture, false, now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
		if err := nextCommand.Complete(now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
	}

	m.commands[nextCommand.ID] = nextCommand
	m.gates[nextGate.ID] = nextGate
	return cloneCommand(nextCommand), nextGate, nil
}

func cloneCommand(command domain.GateCommand) domain.GateCommand {
	if command.Interlock == nil {
		return command
	}
	decision := *command.Interlock
	command.Interlock = &decision
	return command
}

func cloneQueue(queue domain.ReviewQueue) domain.ReviewQueue {
	entries := make([]domain.ReviewQueueEntry, len(queue.Entries))
	copy(entries, queue.Entries)
	queue.Entries = entries
	return queue
}
