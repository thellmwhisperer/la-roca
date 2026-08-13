package service

import (
	"context"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/ingest"
	"github.com/thellmwhisperer/la-roca/internal/store/search"
)

// IngestRequest is what the operator asked for.
type IngestRequest struct {
	// DryRun reports what would be read and writes nothing.
	DryRun bool
	// ExportPath is one extracted account export selected for this invocation.
	// It is never retained in configuration or reused by a later ingest.
	ExportPath string
}

// IngestResult is the run's report, with the search index's own beside it: what
// was just ingested is only answerable once it is indexed, so the two travel
// together.
type IngestResult struct {
	ingest.Result
	Index          *search.Report `json:"index,omitempty"`
	TotalElapsedMS int64          `json:"-"`
}

// Index leaves the database ready to search. It is idempotent and cheap when
// there is nothing new, which is what allows init to call it without punishing
// whoever already had it indexed.
func (s *Service) Index(ctx context.Context) (search.Report, error) {
	if s.opts.ReadOnly {
		return search.Report{}, errReadOnly
	}
	prepared, err := s.ensureSchema(ctx)
	if err != nil {
		return search.Report{}, err
	}
	report, err := search.Index(ctx, s.db, s.opts.Progress)
	report.LexicalBuilt = report.LexicalBuilt || prepared.LexicalBuilt
	report.ElapsedMS += prepared.ElapsedMS
	return report, err
}

// Ingest reads every source of the matrix once and leaves what it wrote
// answerable.
//
// The index is refreshed in the same command and not left to the operator. It is
// incremental, so on a run that wrote nothing it costs nothing, and skipping it
// would leave a memory that is in the database and cannot be found: the worst of
// the two states, because it looks like data loss.
func (s *Service) Ingest(ctx context.Context, req IngestRequest) (IngestResult, error) {
	started := time.Now()
	if s.opts.ReadOnly && !req.DryRun {
		return IngestResult{}, errReadOnly
	}
	var prepared search.Report
	if !req.DryRun {
		var err error
		prepared, err = s.ensureSchema(ctx)
		if err != nil {
			return IngestResult{}, err
		}
	}

	roots := s.opts.Sources
	if req.ExportPath != "" {
		roots = ingest.WithExportPath(roots, req.ExportPath)
	}
	report, err := ingest.Run(ctx, s.db, s.registry, ingest.Options{
		Roots:        roots,
		DryRun:       req.DryRun,
		Progress:     s.opts.Progress,
		LiveProgress: s.opts.IngestProgress,
	})
	result := IngestResult{Result: report}
	if err != nil {
		result.TotalElapsedMS = time.Since(started).Milliseconds()
		return result, err
	}
	if req.DryRun {
		result.TotalElapsedMS = time.Since(started).Milliseconds()
		return result, nil
	}

	index, err := s.Index(ctx)
	if err != nil {
		// The total is reported on every other exit, including the dry run above.
		result.TotalElapsedMS = time.Since(started).Milliseconds()
		return result, err
	}
	index.LexicalBuilt = index.LexicalBuilt || prepared.LexicalBuilt
	index.ElapsedMS += prepared.ElapsedMS
	result.Index = &index
	result.TotalElapsedMS = time.Since(started).Milliseconds()

	return result, nil
}
