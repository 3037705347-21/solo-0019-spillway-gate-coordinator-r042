package storage

import (
	"context"
	"time"

	"example.com/spillway-gate-coordinator/internal/domain"
)

type Repository interface {
	CreateGate(ctx context.Context, gate domain.Gate) error
	Gate(ctx context.Context, id string) (domain.Gate, error)
	ReviewQueue(ctx context.Context, gateID string) (domain.ReviewQueue, error)
	CreateCommand(ctx context.Context, command domain.GateCommand) error
	Command(ctx context.Context, id string) (domain.GateCommand, error)
	ReprioritizeCommand(
		ctx context.Context,
		commandID string,
		priority int64,
		basis string,
		now time.Time,
	) (domain.GateCommand, domain.ReviewQueue, error)
	ReviewCommand(
		ctx context.Context,
		commandID string,
		decision domain.InterlockDecision,
		token string,
		now time.Time,
	) (domain.GateCommand, domain.Gate, error)
	ExecuteCommand(
		ctx context.Context,
		commandID string,
		token string,
		operator string,
		simulateFault bool,
		now time.Time,
	) (domain.GateCommand, domain.Gate, error)
}
