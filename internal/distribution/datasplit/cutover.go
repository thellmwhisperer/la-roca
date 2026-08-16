package datasplit

import (
	"context"
	"fmt"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/distribution/bundledplugin"
	"github.com/thellmwhisperer/la-roca/internal/distribution/corpusarchive"
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
}

type HubReport struct {
	Memory rocaops.MemoryCustodyReport
	Corpus corpusarchive.Report
	Legacy LegacyReport
	Ready  bool
}

// PrepareHub runs only the unfinished DATA-2, DATA-3, and DATA-4 custody work.
// Every source read comes from the verified snapshots published by DATA-2;
// the live core database remains untouched.
func PrepareHub(ctx context.Context, options HubOptions) (HubReport, error) {
	if err := options.valid(); err != nil {
		return HubReport{}, err
	}
	memoryReady, err := rocaops.MemoryCustodyCutoverEligible(ctx, options.OpsDatabase)
	if err != nil {
		return HubReport{}, fmt.Errorf("inspect DATA-2 readiness: %w", err)
	}
	corpusReady, err := corpusarchive.CutoverEligible(ctx, options.CorpusDatabase)
	if err != nil {
		return HubReport{}, fmt.Errorf("inspect DATA-3 readiness: %w", err)
	}
	legacyReady, err := legacyCutoverEligible(ctx, options)
	if err != nil {
		return HubReport{}, fmt.Errorf("inspect DATA-4 readiness: %w", err)
	}
	if memoryReady && corpusReady && legacyReady {
		return HubReport{Ready: true}, nil
	}

	report := HubReport{}
	report.Memory, err = rocaops.MigrateMemoryCustody(ctx, rocaops.MemoryCustodyOptions{
		CorePath: options.CoreDatabase, CorpusPath: options.CorpusDatabase,
		OpsPath: options.OpsDatabase, SnapshotDir: options.SnapshotDir, LockPath: options.LockPath,
	})
	if err != nil {
		return report, fmt.Errorf("prepare DATA-2 memory custody: %w", err)
	}
	coreSnapshot := report.Memory.SnapshotPaths["core"]
	corpusSnapshot := report.Memory.SnapshotPaths["plugin:roca-corpus"]
	if coreSnapshot == "" || corpusSnapshot == "" {
		return report, fmt.Errorf("DATA-2 did not publish the core and corpus snapshots")
	}
	if !corpusReady {
		coreDigest, digestErr := corpusarchive.SnapshotDigest(coreSnapshot)
		if digestErr != nil {
			return report, digestErr
		}
		corpusDigest, digestErr := corpusarchive.SnapshotDigest(corpusSnapshot)
		if digestErr != nil {
			return report, digestErr
		}
		report.Corpus, err = corpusarchive.Merge(ctx, options.CorpusDatabase, []corpusarchive.Source{
			{Database: "core", Path: coreSnapshot, SnapshotDigest: coreDigest},
			{Database: "plugin:roca-corpus", Path: corpusSnapshot,
				SnapshotDigest: corpusDigest, ExistingCorpus: true},
		}, corpusarchive.Options{})
		if err != nil {
			return report, fmt.Errorf("prepare DATA-3 corpus custody: %w", err)
		}
	}
	if !legacyReady {
		report.Legacy, err = ImportLegacyOrphans(ctx, LegacyOptions{
			SourceClone: coreSnapshot, CronDatabase: options.CronDatabase,
			OpsDatabase: options.OpsDatabase, CorpusDatabase: options.CorpusDatabase,
		})
		if err != nil {
			return report, fmt.Errorf("prepare DATA-4 legacy custody: %w", err)
		}
	}

	memoryReady, err = rocaops.MemoryCustodyCutoverEligible(ctx, options.OpsDatabase)
	if err == nil {
		corpusReady, err = corpusarchive.CutoverEligible(ctx, options.CorpusDatabase)
	}
	if err == nil {
		legacyReady, err = legacyCutoverEligible(ctx, options)
	}
	if err != nil {
		return report, fmt.Errorf("recheck DATA SPLIT readiness: %w", err)
	}
	report.Ready = memoryReady && corpusReady && legacyReady
	if !report.Ready {
		return report, fmt.Errorf("DATA SPLIT destinations did not reach cutover eligibility")
	}
	return report, nil
}

func (options HubOptions) valid() error {
	for name, path := range map[string]string{
		"core": options.CoreDatabase, "ops": options.OpsDatabase,
		"corpus": options.CorpusDatabase, "cron": options.CronDatabase,
		"snapshots": options.SnapshotDir,
	} {
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("DATA-6 needs a %s path", name)
		}
	}
	return nil
}

func legacyCutoverEligible(ctx context.Context, options HubOptions) (bool, error) {
	source, err := bundledplugin.OpenDatabase(options.CoreDatabase, true)
	if err != nil {
		return false, err
	}
	defer source.Close()
	undisposed, present, err := inspectSourceInventory(ctx, source)
	if err != nil || len(undisposed) != 0 {
		return false, err
	}
	if present["messages"] {
		count, err := tableCount(ctx, source, "messages")
		if err != nil || count != 0 {
			return false, err
		}
	}
	destinations, err := openDestinations(LegacyOptions{
		SourceClone: options.CoreDatabase, CronDatabase: options.CronDatabase,
		OpsDatabase: options.OpsDatabase, CorpusDatabase: options.CorpusDatabase,
	})
	if err != nil {
		return false, err
	}
	defer closeDatabases(destinations)
	for _, plan := range legacyPlans {
		if !present[plan.sourceTable] {
			continue
		}
		expected, err := tableCount(ctx, source, plan.sourceTable)
		if err != nil {
			return false, err
		}
		var batches, rows, memberships int
		db := destinations[plan.destination]
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(row_count), 0)
			FROM migration_batches WHERE migration = ? AND source_database = 'core' AND source_table = ?`,
			plan.migration, plan.sourceTable).Scan(&batches, &rows); err != nil {
			return false, err
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM custody_memberships
			WHERE migration = ? AND source_database = 'core' AND source_table = ?`,
			plan.migration, plan.sourceTable).Scan(&memberships); err != nil {
			return false, err
		}
		if batches == 0 || rows != expected || memberships != expected {
			return false, nil
		}
	}
	return true, nil
}
