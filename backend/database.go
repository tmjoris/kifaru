package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed schema.sql
var schema string

//go:embed migrations/*.sql
var migrationFiles embed.FS

//go:embed data/kenyan_banks.json
var kenyanBanksJSON []byte

var kenyanBanks = loadKenyanBanks()

func loadKenyanBanks() []Institution {
	var directory struct {
		Institutions []Institution `json:"institutions"`
	}
	if err := json.Unmarshal(kenyanBanksJSON, &directory); err != nil {
		panic(fmt.Sprintf("parse data/kenyan_banks.json: %v", err))
	}
	return directory.Institutions
}

func loadStandard() (map[string]any, Standard, error) {
	path := filepath.Join("data", "kifaru_risk_codes.json")
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, Standard{}, fmt.Errorf("read %s: %w", path, err)
	}
	var raw map[string]any
	var standard Standard
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, Standard{}, err
	}
	if err := json.Unmarshal(body, &standard); err != nil {
		return nil, Standard{}, err
	}
	return raw, standard, nil
}

func (a *App) initDB(ctx context.Context) error {
	for _, statement := range strings.Split(schema, ";") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if _, err := a.db.Exec(ctx, statement); err != nil {
			return err
		}
	}
	if err := a.runMigrations(ctx); err != nil {
		return err
	}
	institutions := append([]Institution{{
		Code: "external", LegalName: "External financial network", Type: "external", Threshold: 0.50,
	}}, kenyanBanks...)
	for _, institution := range institutions {
		// Directory names and types follow the committed source on every start;
		// administrator-adjusted thresholds remain unchanged.
		if _, err := a.db.Exec(ctx, `
			INSERT INTO institutions(code,name,type,threshold,active) VALUES ($1,$2,$3,$4,1)
			ON CONFLICT (code) DO UPDATE SET name=EXCLUDED.name,type=EXCLUDED.type,active=1`,
			institution.Code, valueOr(institution.LegalName, institution.Name), institution.Type,
			institution.Threshold); err != nil {
			return err
		}
	}
	if err := a.seedDemoUsers(ctx); err != nil {
		return err
	}
	constraints := []string{
		`DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='reports_reporting_institution_fkey') THEN
				ALTER TABLE reports ADD CONSTRAINT reports_reporting_institution_fkey
				FOREIGN KEY (reporting_institution) REFERENCES institutions(code) NOT VALID;
			END IF;
		END $$`,
		`DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='reports_destination_institution_fkey') THEN
				ALTER TABLE reports ADD CONSTRAINT reports_destination_institution_fkey
				FOREIGN KEY (destination_institution) REFERENCES institutions(code) NOT VALID;
			END IF;
		END $$`,
		`DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='alerts_receiving_institution_fkey') THEN
				ALTER TABLE alerts ADD CONSTRAINT alerts_receiving_institution_fkey
				FOREIGN KEY (receiving_institution) REFERENCES institutions(code) NOT VALID;
			END IF;
		END $$`,
		`DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='alerts_reporting_institution_fkey') THEN
				ALTER TABLE alerts ADD CONSTRAINT alerts_reporting_institution_fkey
				FOREIGN KEY (reporting_institution) REFERENCES institutions(code) NOT VALID;
			END IF;
		END $$`,
	}
	for _, statement := range constraints {
		if _, err := a.db.Exec(ctx, statement); err != nil {
			return err
		}
	}
	defaults := map[string]any{
		"validated_threshold":    defaultValidated,
		"insufficient_threshold": defaultInsufficient,
		"enabled_sources":        []string{"soc_connector", "rest", "webhook", "batch"},
		"agent_version":          agentVersion,
		"configuration_version":  1,
	}
	for key, value := range defaults {
		encoded, _ := json.Marshal(value)
		if _, err := a.db.Exec(ctx, `
			INSERT INTO config(key,value) VALUES ($1,$2)
			ON CONFLICT (key) DO NOTHING`, key, string(encoded)); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) runMigrations(ctx context.Context) error {
	if _, err := a.db.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		var applied bool
		if err := a.db.QueryRow(ctx,
			"SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)", entry.Name(),
		).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		body, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		tx, err := a.db.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, string(body)); err == nil {
			_, err = tx.Exec(ctx,
				"INSERT INTO schema_migrations(version,applied_at) VALUES ($1,$2)",
				entry.Name(), utcNow())
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}
