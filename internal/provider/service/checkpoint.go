package service

import (
	"context"

	"github.com/thellmwhisperer/la-roca/internal/store"
)

// Checkpoint bounds the resident's write tail. A busy checkpoint is harmless:
// the next call retries it, while a live reader never gets to hold a service
// read transaction beyond its request.
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
