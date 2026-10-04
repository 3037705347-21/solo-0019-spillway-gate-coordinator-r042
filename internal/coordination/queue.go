package coordination

import (
	"context"

	"example.com/spillway-gate-coordinator/internal/contracts"
	"example.com/spillway-gate-coordinator/internal/domain"
)

func (s *Service) ReviewQueue(ctx context.Context, gateID string) (domain.ReviewQueue, error) {
	return s.repository.ReviewQueue(ctx, gateID)
}

func (s *Service) EnqueueCommand(ctx context.Context, commandID string) (domain.ReviewQueue, error) {
	return s.repository.EnqueueCommand(ctx, commandID)
}

func (s *Service) ReprioritizeCommand(
	ctx context.Context,
	commandID string,
	request contracts.ReprioritizeCommandRequest,
) (domain.ReviewQueue, error) {
	return s.repository.ReprioritizeCommand(
		ctx,
		commandID,
		request.Priority,
		request.Basis,
		request.ChangedBy,
		s.now(),
	)
}
