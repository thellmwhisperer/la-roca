//go:build acceptance

package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// hybridHome is a real `roca init` over a HOME holding one Claude session, so
// the exchange lands in the bundled corpus the way an operator's history does.
func (m *world) hybridHome(phrase string) error {
	session := filepath.Join(m.home, ".claude", "projects", "-unknown-absolute-project",
		claudeAcceptanceSession+".jsonl")
	body := fmt.Sprintf("{\"type\":\"user\",\"timestamp\":\"2026-08-01T10:00:10Z\",\"message\":{\"content\":\"where is the %s\"}}\n"+
		"{\"type\":\"assistant\",\"timestamp\":\"2026-08-01T10:00:11Z\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"answer\"}]}}\n",
		phrase)
	if err := writeFixture(session, body); err != nil {
		return err
	}
	if err := m.runInit(); err != nil {
		return err
	}
	if m.last.code != 0 {
		return fmt.Errorf("init exited %d: %s%s", m.last.code, m.last.stdout, m.last.stderr)
	}
	if _, err := m.runWith("roca exec", []string{"exec", "--json", "--timeout-ms", "5000",
		"SELECT id FROM plugin_roca_corpus.exchanges WHERE human_text LIKE '%" + phrase + "%'"}); err != nil {
		return err
	}
	var result struct {
		Rows []struct {
			ID json.Number `json:"id"`
		} `json:"rows"`
	}
	decoder := json.NewDecoder(strings.NewReader(m.last.stdout))
	decoder.UseNumber()
	if err := decoder.Decode(&result); err != nil || len(result.Rows) != 1 {
		return fmt.Errorf("find the ingested exchange (%v): %s%s", err, m.last.stdout, m.last.stderr)
	}
	m.exchangeID = result.Rows[0].ID.String()
	return nil
}

// stubVectorLeg stands in for the companion so no scenario loads the model: it
// answers `query` with the exchange as its only hit, named as the companion
// names it.
func (m *world) stubVectorLeg() error {
	hit := fmt.Sprintf(`{"vector_executed":true,"results":[{"rank":1,"score":0.9,"database":"corpus","table":"exchanges","id":"%s","text":"the fixture exchange"}]}`,
		m.exchangeID)
	script := "#!/bin/sh\ncase \" $* \" in *\" query \"*) printf '%s\\n' '" + hit + "' ;; *) exit 1 ;; esac\n"
	path := filepath.Join(m.home, "bin", "roca-vector")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(script), 0o700)
}

type hybridHit struct {
	Database  string   `json:"database"`
	Table     string   `json:"table"`
	ID        string   `json:"id"`
	Legs      []string `json:"legs"`
	Consensus bool     `json:"consensus"`
}

func (m *world) exchangeHits() ([]hybridHit, error) {
	var result struct {
		Hits []hybridHit `json:"hits"`
	}
	if err := json.Unmarshal([]byte(m.last.stdout), &result); err != nil {
		return nil, fmt.Errorf("decode %q: %w\n%s", m.last.command, err, m.last.stdout)
	}
	var found []hybridHit
	for _, hit := range result.Hits {
		if hit.Table == "exchanges" && hit.ID == m.exchangeID {
			found = append(found, hit)
		}
	}
	return found, nil
}

func (m *world) oneFusedExchangeHit(database string) error {
	found, err := m.exchangeHits()
	if err != nil {
		return err
	}
	if len(found) != 1 || found[0].Database != database || !found[0].Consensus ||
		!slices.Contains(found[0].Legs, "fts") || !slices.Contains(found[0].Legs, "vector") {
		return fmt.Errorf("exchange %s hits = %+v, want one from %s with fts, vector and consensus:\n%s",
			m.exchangeID, found, database, m.last.stdout)
	}
	return nil
}

func (m *world) exchangeHitWithConsensus() error {
	found, err := m.exchangeHits()
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(found, func(hit hybridHit) bool { return hit.Consensus }) {
		return fmt.Errorf("no exchange %s hit with consensus:\n%s", m.exchangeID, m.last.stdout)
	}
	return nil
}
