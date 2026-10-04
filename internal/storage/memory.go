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

	queue, exists := m.queues[command.GateID]
	if !exists {
		// Defensive: queues are created together with gates.
		queue = domain.NewReviewQueue(command.GateID, command.CreatedAt)
	}
	if _, err := queue.Enqueue(
		command.ID,
		domain.DefaultPriority,
		domain.InitialQueueBasis,
		command.RequestedBy,
		command.CreatedAt,
	); err != nil {
		return err
	}

	m.commands[command.ID] = command
	m.queues[command.GateID] = queue
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

func (m *MemoryRepository) ReviewQueue(_ context.Context, gateID string) (domain.ReviewQueue, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	queue, exists := m.queues[gateID]
	if !exists {
		if _, gateExists := m.gates[gateID]; !gateExists {
			return domain.ReviewQueue{}, domain.NotFound("gate_not_found", "gate id was not found")
		}
		return domain.ReviewQueue{}, domain.Internal("review_queue_missing", "review queue is unavailable")
	}
	return queue, nil
}

func (m *MemoryRepository) EnqueueCommand(_ context.Context, commandID string) (domain.ReviewQueue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	command, exists := m.commands[commandID]
	if !exists {
		return domain.ReviewQueue{}, domain.NotFound("command_not_found", "command id was not found")
	}
	queue, exists := m.queues[command.GateID]
	if !exists {
		return domain.ReviewQueue{}, domain.NotFound("gate_not_found", "gate id was not found")
	}
	// Only commands still waiting for review can be queued. A command that
	// has already entered another state keeps its history untouched.
	if command.Status != domain.CommandPending {
		return domain.ReviewQueue{}, domain.Conflict("command_not_pending", "only pending commands can wait in the review queue")
	}
	if _, err := queue.Enqueue(
		command.ID,
		domain.DefaultPriority,
		domain.InitialQueueBasis,
		command.RequestedBy,
		command.UpdatedAt,
	); err != nil {
		return domain.ReviewQueue{}, err
	}
	m.queues[command.GateID] = queue
	return queue, nil
}

func (m *MemoryRepository) ReprioritizeCommand(
	_ context.Context,
	commandID string,
	priority int,
	basis string,
	changedBy string,
	now time.Time,
) (domain.ReviewQueue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	command, exists := m.commands[commandID]
	if !exists {
		return domain.ReviewQueue{}, domain.NotFound("command_not_found", "command id was not found")
	}
	queue, exists := m.queues[command.GateID]
	if !exists {
		return domain.ReviewQueue{}, domain.NotFound("gate_not_found", "gate id was not found")
	}
	// Adjustments cannot reach a command that has already left review,
	// whether through rejection, approval, or execution.
	if command.Status != domain.CommandPending {
		return domain.ReviewQueue{}, domain.Conflict("command_not_pending", "only pending commands can be reprioritized")
	}
	if _, err := queue.Reprioritize(command.ID, priority, basis, changedBy, now); err != nil {
		return domain.ReviewQueue{}, err
	}
	m.queues[command.GateID] = queue
	return queue, nil
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
	queue, exists := m.queues[command.GateID]
	if !exists {
		return domain.GateCommand{}, domain.Gate{}, domain.NotFound("gate_not_found", "gate id was not found")
	}

	// Gate the review on the real pending precondition first, so commands
	// already in another state always report the committed fact.
	if command.Status != domain.CommandPending {
		return domain.GateCommand{}, domain.Gate{}, domain.Conflict("command_not_pending", "only pending commands can be reviewed")
	}

	// Drop any entries whose commands left the pending state before we
	// decide who the current head is.
	queue.RetainPending(func(id string) bool {
		candidate, ok := m.commands[id]
		return ok && candidate.Status == domain.CommandPending
	}, now)

	// The head gate is evaluated before the decision is applied and before
	// the gate is touched, so a non-head review always fails as a queue
	// violation regardless of who reserved the gate concurrently.
	head, hasHead := queue.Head()
	if !hasHead || head.CommandID != commandID {
		return domain.GateCommand{}, domain.Gate{}, domain.Conflict(
			"command_not_queue_head",
			"only the current head of the gate review queue can be reviewed",
		)
	}

	nextCommand := cloneCommand(command)
	if err := nextCommand.Review(decision, token, now); err != nil {
		return domain.GateCommand{}, domain.Gate{}, err
	}

	nextGate := gate
	if decision.IsClear() {
		if err := nextGate.Reserve(nextCommand.ID, now); err != nil {
			return domain.GateCommand{}, domain.Gate{}, err
		}
	}
	// Commit the review facts and release the head together. The next
	// head is recomputed from the entries that remain genuinely pending.
	queue.Remove(nextCommand.ID, now)
	queue.RetainPending(func(id string) bool {
		candidate, ok := m.commands[id]
		return ok && candidate.Status == domain.CommandPending
	}, now)

	m.commands[nextCommand.ID] = nextCommand
	m.gates[nextGate.ID] = nextGate
	m.queues[nextGate.ID] = queue
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
