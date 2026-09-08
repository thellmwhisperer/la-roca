package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"time"
)

type vectorDoctorReport struct {
	Databases []vectorDoctorDatabase `json:"databases,omitempty"`
	Remedies  []string               `json:"remedies,omitempty"`
}

type vectorDoctorDatabase struct {
	Plugin             string `json:"plugin"`
	Database           string `json:"database"`
	EmbeddedChunks     *int64 `json:"embedded_chunks"`
	CandidateChunks    *int64 `json:"candidate_chunks"`
	SidecarBytes       *int64 `json:"sidecar_bytes"`
	State              string `json:"state"`
	IndexLock          string `json:"index_lock,omitempty"`
	CompactRecommended bool   `json:"compact_recommended"`
}

func (env *cliEnv) collectVectorDoctor(ctx context.Context) *vectorDoctorReport {
	paths, err := env.resolvePaths()
	if err != nil {
		return nil
	}
	companion, found := resolveCompanion("vector", pluginExecutableDir(paths))
	if !found {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	args := []string{"--json", "status"}
	if env.dbPath != "" {
		args = append([]string{"--db-path", env.dbPath}, args...)
	}
	command := exec.CommandContext(ctx, companion, args...)
	var stdout bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &bytes.Buffer{}
	if err := command.Run(); err != nil {
		return nil
	}
	var envelope struct {
		Databases []vectorDoctorDatabase `json:"databases"`
		Help      []string               `json:"help"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		return nil
	}
	if len(envelope.Databases) == 0 {
		return nil
	}
	report := &vectorDoctorReport{Databases: envelope.Databases}
	for _, line := range envelope.Help {
		if line == "" || line == "Run `roca vector status --json` for the complete result envelope" {
			continue
		}
		report.Remedies = append(report.Remedies, line)
	}
	return report
}

func renderVectorDoctor(env *cliEnv, report *vectorDoctorReport) {
	if report == nil || len(report.Databases) == 0 {
		return
	}
	env.print("vector sidecars:")
	for _, row := range report.Databases {
		chunks := "unknown"
		if row.EmbeddedChunks != nil {
			chunks = strconv.FormatInt(*row.EmbeddedChunks, 10)
		}
		bytes := "unknown"
		if row.SidecarBytes != nil {
			bytes = strconv.FormatInt(*row.SidecarBytes, 10)
		}
		lock := row.IndexLock
		if lock == "" {
			lock = "unknown"
		}
		line := "  %s/%s · %s chunks · %s bytes · state %s · lock %s"
		if row.CompactRecommended {
			line += " · compact recommended"
		}
		env.print(line, row.Plugin, row.Database, chunks, bytes, orDash(row.State), lock)
	}
	for _, remedy := range report.Remedies {
		env.print("      remedy: %s", remedy)
	}
}
