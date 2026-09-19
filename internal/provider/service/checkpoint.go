package service

import (
	"context"

	"github.com/thellmwhisperer/la-roca/internal/store"
)

// Checkpoint attempts maintenance on each distinct service database. Errors
// are ignored so maintenance cannot turn a committed write into a failure.
func (s *Service) Checkpoint(ctx context.Context) error {
	if s == nil {
		return nil
	}
	seen := map[*store.DB]bool{}
	for _, database := range []*store.DB{s.db, s.legacy, s.hubDB, s.ops, s.corpus, s.layerDB} {
		if database == nil || seen[database] {
			continue
		}
		seen[database] = true
		_ = database.Checkpoint(ctx)
	}
	return nil
}
