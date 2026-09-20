package datasplit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/thellmwhisperer/la-roca/internal/distribution/corpusarchive"
	"github.com/thellmwhisperer/la-roca/internal/distribution/migrationledger"
	"github.com/thellmwhisperer/la-roca/internal/distribution/rocaops"
)

// HubOptions names the immutable core source and every plugin destination that
// must be verified before DATA-6 may serve the federation hub.
type HubOptions struct {
	CoreDatabase   string
	OpsDatabase    string
	CorpusDatabase string
	CronDatabase   string
	SnapshotDir    string
	LockPath       string
	// Progress, when set, observes every migration stage so a driver can
	// print what an operator needs to tell "working" from "stuck"
	// (issue #455). Batch counts from one within the stage it belongs to.
	Progress func(HubProgress) error
}

// Hub stage names, stable for operators and tests.
const (
	StageMemorySnapshot   = "data2-snapshot"
	StageMemoryImport     = "data2-import"
	StageMemoryFTSRebuild = "data2-fts-rebuild"
	StageMemoryVerify     = "data2-verify"
	StageCorpusMerge      = "data3-corpus"
	StageLegacyImport     = "data4-legacy"
)

// HubProgress is one observable step of a DATA SPLIT migration. Source names
// the database the step concerns; Batch and Batches are zero except on the
// DATA-2 import stage, where they count committed batches against the planned
// total.
type HubProgress struct {
	Stage   string
	Source  string
	Detail  string
	Batch   int
	Batches int
	Rows    int
}

type HubReport struct {
	Memory rocaops.MemoryCustodyReport
	Corpus corpusarchive.Report
	Legacy LegacyReport
	Ready  bool
}

type hubEligibility struct {
	memory bool
	corpus bool
	legacy bool
}

// Migrate runs only the unfinished DATA-2, DATA-3, and DATA-4 custody work.
// Every source read comes from the verified snapshots published by DATA-2;
// the live core database remains untouched.
func Migrate(ctx context.Context, options HubOptions) (HubReport, error) {
	if err := options.valid(); err != nil {
		return HubReport{}, err
	}
	eligibility, err := inspectHubEligibility(ctx, options)
	if err != nil {
		return HubReport{}, err
	}
	if eligibility.ready() {
		return HubReport{Ready: true}, nil
	}
	report := HubReport{}
	memoryOptions := rocaops.MemoryCustodyOptions{
		CorePath: options.CoreDatabase, CorpusPath: options.CorpusDatabase,
		OpsPath: options.OpsDatabase, SnapshotDir: options.SnapshotDir, LockPath: options.LockPath,
	}
	if options.Progress != nil {
		memoryOptions.Progress = func(step rocaops.MemoryCustodyProgress) error {
			return options.Progress(HubProgress{Stage: memoryStage(step.Stage), Source: step.Source,
				Detail: step.Detail, Batch: step.Batch, Batches: step.Batches,
				Rows: step.Rows})
		}
		memoryOptions.AfterBatch = func(batch rocaops.MemoryBatch) error {
			return options.Progress(HubProgress{Stage: StageMemoryImport,
				Source: batch.SourceDatabase, Batch: batch.Batch,
				Batches: batch.Batches, Rows: batch.RowCount})
		}
		if err := options.Progress(HubProgress{Stage: StageMemorySnapshot,
			Detail: "freezing and importing memory custody from the three sources"}); err != nil {
			return report, fmt.Errorf("prepare DATA-2 memory custody: %w", err)
		}
	}
	report.Memory, err = rocaops.MigrateMemoryCustody(ctx, memoryOptions)
	if err != nil {
		return report, fmt.Errorf("prepare DATA-2 memory custody: %w", err)
	}
	coreSnapshot := report.Memory.SnapshotPaths["core"]
	corpusSnapshot := report.Memory.SnapshotPaths["plugin:roca-corpus"]
	if coreSnapshot == "" || corpusSnapshot == "" {
		return report, fmt.Errorf("DATA-2 did not publish the core and corpus snapshots")
	}
	coreDigest, digestErr := corpusarchive.SnapshotDigest(coreSnapshot)
	if digestErr != nil {
		return report, digestErr
	}
	corpusDigest, digestErr := corpusarchive.SnapshotDigest(corpusSnapshot)
	if digestErr != nil {
		return report, digestErr
	}
	corpusSources := []corpusarchive.Source{
		{Database: "core", Path: coreSnapshot, SnapshotDigest: coreDigest},
		{Database: "plugin:roca-corpus", Path: corpusSnapshot,
			SnapshotDigest: corpusDigest, ExistingCorpus: true},
	}
	if !eligibility.corpus {
		if err := options.progressStage(StageCorpusMerge,
			"merging the frozen core and corpus snapshots into corpus custody"); err != nil {
			return report, fmt.Errorf("prepare DATA-3 corpus custody: %w", err)
		}
		report.Corpus, err = corpusarchive.Merge(ctx, options.CorpusDatabase, []corpusarchive.Source{
			corpusSources[0], corpusSources[1],
		}, corpusarchive.Options{})
		if err != nil {
			return report, fmt.Errorf("prepare DATA-3 corpus custody: %w", err)
		}
	}

	if !eligibility.legacy {
		if err := options.progressStage(StageLegacyImport,
			"importing legacy orphans beside corpus custody"); err != nil {
			return report, fmt.Errorf("prepare DATA-4 legacy custody: %w", err)
		}
		report.Legacy, err = ImportLegacyOrphans(ctx, LegacyOptions{
			SourceClone: coreSnapshot, CronDatabase: options.CronDatabase,
			OpsDatabase: options.OpsDatabase, CorpusDatabase: options.CorpusDatabase,
		})
		if err != nil {
			return report, fmt.Errorf("prepare DATA-4 legacy custody: %w", err)
		}
	}

	eligibility, err = inspectHubEligibility(ctx, options)
	if err != nil {
		return report, fmt.Errorf("recheck DATA SPLIT readiness: %w", err)
	}
	report.Ready = eligibility.ready()
	if !report.Ready {
		return report, fmt.Errorf("DATA SPLIT destinations did not reach cutover eligibility")
	}
	return report, nil
}

// memoryStage names the DATA-2 stage a custody progress step belongs to.
func memoryStage(stage rocaops.MemoryCustodyStage) string {
	switch stage {
	case rocaops.StageSnapshot:
		return StageMemorySnapshot
	case rocaops.StageImport:
		return StageMemoryImport
	case rocaops.StageFTSRebuild:
		return StageMemoryFTSRebuild
	case rocaops.StageVerify:
		return StageMemoryVerify
	default:
		return StageMemorySnapshot
	}
}

// progressStage reports one hub-wide stage, tolerating the absence of an
// observer, and wraps a refusal the way every other stage failure reports.
func (options HubOptions) progressStage(stage, detail string) error {
	if options.Progress == nil {
		return nil
	}
	if err := options.Progress(HubProgress{Stage: stage, Detail: detail}); err != nil {
		return fmt.Errorf("progress observer: %w", err)
	}
	return nil
}

func HubCutoverEligible(ctx context.Context, options HubOptions,
	timeout ...time.Duration) (bool, error) {
	if err := options.validDatabases(); err != nil {
		return false, err
	}
	eligibility, err := inspectHubEligibility(ctx, options, timeout...)
	if err != nil {
		return false, err
	}
	return eligibility.ready(), nil
}

func inspectHubEligibility(ctx context.Context, options HubOptions,
	timeout ...time.Duration) (hubEligibility, error) {
	var eligibility hubEligibility
	var err error
	eligibility.memory, err = rocaops.MemoryCustodyCutoverEligible(
		ctx, options.OpsDatabase, timeout...)
	if err != nil {
		return eligibility, fmt.Errorf("inspect DATA-2 readiness: %w", err)
	}
	eligibility.corpus, err = corpusarchive.CutoverEligible(
		ctx, options.CorpusDatabase, timeout...)
	if err != nil {
		return eligibility, fmt.Errorf("inspect DATA-3 readiness: %w", err)
	}
	eligibility.legacy, err = LegacyCutoverEligible(ctx, options, timeout...)
	if err != nil {
		return eligibility, fmt.Errorf("inspect DATA-4 readiness: %w", err)
	}
	return eligibility, nil
}

func (eligibility hubEligibility) ready() bool {
	return eligibility.memory && eligibility.corpus && eligibility.legacy
}

func (options HubOptions) valid() error {
	if err := options.validDatabases(); err != nil {
		return err
	}
	if strings.TrimSpace(options.SnapshotDir) == "" {
		return fmt.Errorf("DATA-6 needs a snapshots path")
	}
	return nil
}

func (options HubOptions) validDatabases() error {
	for name, path := range map[string]string{
		"core": options.CoreDatabase, "ops": options.OpsDatabase,
		"corpus": options.CorpusDatabase, "cron": options.CronDatabase,
	} {
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("DATA-6 needs a %s path", name)
		}
	}
	return nil
}

// LegacyCutoverEligible reads only the four DATA-4 destination ledger entries.
// Reconciliation and source inventory belong to the explicit migration.
func LegacyCutoverEligible(ctx context.Context, options HubOptions,
	timeout ...time.Duration) (bool, error) {
	destinations, err := openDestinations(LegacyOptions{
		CronDatabase: options.CronDatabase, OpsDatabase: options.OpsDatabase,
		CorpusDatabase: options.CorpusDatabase,
	}, true, timeout...)
	if err != nil {
		return false, err
	}
	defer closeDatabases(destinations)
	seen := map[string]bool{}
	for _, plan := range legacyPlans {
		if seen[plan.migration] {
			continue
		}
		seen[plan.migration] = true
		ready, err := migrationledger.MigrationCutoverEligible(ctx, destinations[plan.destination], plan.migration)
		if err != nil || !ready {
			return false, err
		}
	}
	return true, nil
}
