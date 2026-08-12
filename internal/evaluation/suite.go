package evaluation

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/thellmwhisperer/la-roca/internal/store"
)

//go:embed testdata/golden.json testdata/recorded_plans.json testdata/fixture.sql
var files embed.FS

type Case struct {
	ID             string   `json:"id"`
	Category       string   `json:"category"`
	Question       string   `json:"question"`
	ExpectedKind   string   `json:"expected_kind"`
	ExpectedMarker string   `json:"expected_marker"`
	RescuePath     []string `json:"rescue_path,omitempty"`
	Headroom       string   `json:"headroom,omitempty"`
}

type Suite struct {
	SchemaVersion int             `json:"schema_version"`
	Fixture       string          `json:"fixture"`
	Cases         []Case          `json:"cases"`
	Provider      string          `json:"-"`
	Model         string          `json:"-"`
	Plans         map[string]Plan `json:"-"`
}

type recordedPlans struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Plans    []struct {
		CaseID string   `json:"case_id"`
		SQL    []string `json:"sql"`
	} `json:"plans"`
}

func LoadSuite() (Suite, error) {
	var suite Suite
	if err := readJSON("testdata/golden.json", &suite); err != nil {
		return suite, err
	}
	var recorded recordedPlans
	if err := readJSON("testdata/recorded_plans.json", &recorded); err != nil {
		return suite, err
	}
	suite.Provider, suite.Model = recorded.Provider, recorded.Model
	suite.Plans = make(map[string]Plan, len(recorded.Plans))
	for _, item := range recorded.Plans {
		if _, exists := suite.Plans[item.CaseID]; exists {
			return suite, fmt.Errorf("recorded plan %q appears more than once", item.CaseID)
		}
		suite.Plans[item.CaseID] = Plan{SQL: item.SQL, Provider: recorded.Provider, Model: recorded.Model}
	}
	if err := validateSuite(suite); err != nil {
		return suite, err
	}
	return suite, nil
}

func readJSON(name string, target any) error {
	raw, err := files.ReadFile(name)
	if err != nil {
		return fmt.Errorf("read embedded %s: %w", name, err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode embedded %s: %w", name, err)
	}
	return nil
}

func validateSuite(suite Suite) error {
	if suite.SchemaVersion != 1 || suite.Fixture == "" || len(suite.Cases) == 0 {
		return fmt.Errorf("golden set metadata is incomplete")
	}
	seen := make(map[string]bool, len(suite.Cases))
	for _, golden := range suite.Cases {
		if golden.ID == "" || golden.Question == "" || golden.ExpectedKind == "" ||
			golden.ExpectedMarker == "" {
			return fmt.Errorf("golden case %q is incomplete", golden.ID)
		}
		if seen[golden.ID] {
			return fmt.Errorf("golden case %q appears more than once", golden.ID)
		}
		seen[golden.ID] = true
		plan, exists := suite.Plans[golden.ID]
		if !exists || len(plan.SQL) != len(golden.RescuePath)+1 {
			return fmt.Errorf("golden case %q has %d questions but %d recorded plans",
				golden.ID, len(golden.RescuePath)+1, len(plan.SQL))
		}
	}
	if len(suite.Plans) != len(suite.Cases) {
		return fmt.Errorf("recorded plans have %d entries for %d golden cases",
			len(suite.Plans), len(suite.Cases))
	}
	return nil
}

func PrepareFixture(ctx context.Context, root string) (string, func(), error) {
	if strings.TrimSpace(root) == "" {
		return "", func() {}, fmt.Errorf("fixture root is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", func() {}, fmt.Errorf("resolve fixture root: %w", err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return "", func() {}, fmt.Errorf("create fixture root: %w", err)
	}
	runDir, err := os.MkdirTemp(abs, "run-")
	if err != nil {
		return "", func() {}, fmt.Errorf("create fixture run: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(runDir) }
	dbPath := filepath.Join(runDir, "synthetic.sqlite")
	db, err := store.Open(dbPath)
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	fail := func(cause error) (string, func(), error) {
		db.Close()
		cleanup()
		return "", func() {}, cause
	}
	if err := store.ApplySchema(ctx, db); err != nil {
		return fail(err)
	}
	if err := store.EnsureSearchSchema(ctx, db); err != nil {
		return fail(err)
	}
	fixture, err := files.ReadFile("testdata/fixture.sql")
	if err != nil {
		return fail(fmt.Errorf("read embedded fixture: %w", err))
	}
	if err := db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, string(fixture))
		return err
	}); err != nil {
		return fail(fmt.Errorf("seed synthetic fixture: %w", err))
	}
	if err := db.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("close synthetic fixture: %w", err)
	}
	return dbPath, cleanup, nil
}
