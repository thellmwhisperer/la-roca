package service

import (
	"context"
	"database/sql"
	"fmt"
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
	report := prepared
	if s.servingLayout() != LayoutCutover {
		legacy, legacyErr := search.Index(ctx, s.db, s.opts.Progress)
		report.LexicalBuilt = report.LexicalBuilt || legacy.LexicalBuilt
		report.ElapsedMS += legacy.ElapsedMS
		err = legacyErr
	}
	if err == nil && s.opts.CorpusEnabled && s.corpus != nil {
		corpus, corpusErr := search.Index(ctx, s.corpus, s.opts.Progress)
		report.LexicalBuilt = report.LexicalBuilt || corpus.LexicalBuilt
		report.ElapsedMS += corpus.ElapsedMS
		err = corpusErr
	}
	return report, err
}

// ingestTarget is the database a run reads its watermarks from and writes to.
//
// Read-only never opens the corpus for writing, and a dry run there still has to
// answer about the database the rows actually live in: reporting core's
// watermarks would preview as pending every file a normal run has already read.
// So it reaches the same corpus through a read-only handle, which a dry run
// never needs to write to.
func (s *Service) ingestTarget() (ingest.Database, func(), error) {
	noop := func() {}
	if !s.opts.CorpusEnabled {
		return s.db, noop, nil
	}
	if s.corpus != nil {
		return s.corpus, noop, nil
	}
	resident := s.residentCorpus()
	if resident == nil {
		return s.db, noop, nil
	}
	handle, err := sql.Open("sqlite", resident.ReadOnlyURI())
	if err != nil {
		return nil, noop, fmt.Errorf("open %s read-only: %w", rocaCorpusPluginName, err)
	}
	return readOnlyIngest{handle: handle}, func() { handle.Close() }, nil
}

// readOnlyIngest carries the corpus into a dry run without a writable handle.
// Every write refuses with the mode's own error rather than reaching the disk.
type readOnlyIngest struct{ handle *sql.DB }

func (r readOnlyIngest) SQL() *sql.DB { return r.handle }

func (r readOnlyIngest) Write(context.Context, func(*sql.Tx) error) error { return errReadOnly }

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
	// The export is classified before anything is prepared, so a directory that
	// is neither vendor's export costs the operator a refusal and not a run.
	roots := s.opts.Sources
	if req.ExportPath != "" {
		selected, err := ingest.WithExportPath(roots, req.ExportPath)
		if err != nil {
			return IngestResult{}, err
		}
		roots = selected
	}
	var prepared search.Report
	if !req.DryRun {
		var err error
		prepared, err = s.ensureSchema(ctx)
		if err != nil {
			return IngestResult{}, err
		}
	}

	target, closeTarget, err := s.ingestTarget()
	if err != nil {
		return IngestResult{}, err
	}
	defer closeTarget()
	var hermesReservedMemories *sql.DB
	if s.ops != nil {
		hermesReservedMemories, err = s.ops.ReadOnly()
		if err != nil {
			return IngestResult{}, fmt.Errorf("open %s for Hermes memory deduplication: %w",
				rocaOpsPluginName, err)
		}
	}
	report, err := ingest.Run(ctx, target, s.registry, ingest.Options{
		Roots:                  roots,
		HermesReservedMemories: hermesReservedMemories,
		Ops:                    s.ops,
		DryRun:                 req.DryRun,
		Progress:               s.opts.Progress,
		LiveProgress:           s.opts.IngestProgress,
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
