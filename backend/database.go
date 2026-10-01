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
	institutions := [][]any{
		{"external", "External financial network", "external", 0.50},
		{"bank_a", "NCBA Bank Kenya PLC", "bank", 0.45},
		{"bank_b", "KCB Bank Kenya Limited", "bank", 0.50},
		{"psp_c", "Equity Bank Kenya Limited", "bank", 0.40},
		{"sacco_d", "I&M Bank Limited", "bank", 0.60},
		{"ke:absa-bank-kenya", "Absa Bank Kenya PLC", "bank", 0.50},
		{"ke:access-bank-kenya", "Access Bank (Kenya) PLC", "bank", 0.50},
		{"ke:bank-of-africa-kenya", "Bank of Africa Kenya Limited", "bank", 0.50},
		{"ke:bank-of-baroda-kenya", "Bank of Baroda (Kenya) Limited", "bank", 0.50},
		{"ke:bank-of-india-kenya", "Bank of India (Kenya)", "bank", 0.50},
		{"ke:citibank-n-a-kenya", "Citibank N.A. Kenya", "bank", 0.50},
		{"ke:commercial-international-bank-kenya-cib", "Commercial International Bank Kenya Limited", "bank", 0.50},
		{"ke:consolidated-bank-of-kenya", "Consolidated Bank of Kenya Limited", "bank", 0.50},
		{"ke:co-operative-bank-of-kenya", "Co-operative Bank of Kenya Limited", "bank", 0.50},
		{"ke:credit-bank", "Credit Bank PLC", "bank", 0.50},
		{"ke:development-bank-of-kenya", "Development Bank of Kenya Limited", "bank", 0.50},
		{"ke:diamond-trust-bank-dtb", "Diamond Trust Bank Kenya Limited", "bank", 0.50},
		{"ke:dib-bank-kenya", "DIB Bank Kenya Limited", "bank", 0.50},
		{"ke:ecobank-kenya", "Ecobank Kenya Limited", "bank", 0.50},
		{"ke:family-bank", "Family Bank Limited", "bank", 0.50},
		{"ke:first-community-bank", "First Community Bank Limited", "bank", 0.50},
		{"ke:guaranty-trust-bank-kenya-gtbank", "Guaranty Trust Bank (Kenya) Limited", "bank", 0.50},
		{"ke:guardian-bank", "Guardian Bank Limited", "bank", 0.50},
		{"ke:gulf-african-bank", "Gulf African Bank Limited", "bank", 0.50},
		{"ke:habib-bank-ag-zurich", "Habib Bank AG Zurich", "bank", 0.50},
		{"ke:hfc-limited-housing-finance", "Housing Finance Company of Kenya Limited", "bank", 0.50},
		{"ke:kingdom-bank", "Kingdom Bank Limited", "bank", 0.50},
		{"ke:middle-east-bank-kenya", "Middle East Bank (Kenya) Limited", "bank", 0.50},
		{"ke:m-oriental-bank", "M Oriental Bank Limited", "bank", 0.50},
		{"ke:national-bank-of-kenya", "National Bank of Kenya Limited", "bank", 0.50},
		{"ke:paramount-bank", "Paramount Bank Limited", "bank", 0.50},
		{"ke:prime-bank", "Prime Bank Limited", "bank", 0.50},
		{"ke:sbm-bank-kenya", "SBM Bank Kenya Limited", "bank", 0.50},
		{"ke:sidian-bank", "Sidian Bank Limited", "bank", 0.50},
		{"ke:stanbic-bank-kenya", "Stanbic Bank Kenya Limited", "bank", 0.50},
		{"ke:standard-chartered-bank-kenya", "Standard Chartered Bank Kenya Limited", "bank", 0.50},
		{"ke:uba-kenya", "United Bank for Africa Kenya Limited", "bank", 0.50},
		{"ke:victoria-commercial-bank", "Victoria Commercial Bank PLC", "bank", 0.50},
		{"ke:abc-bank-african-banking-corporation", "African Banking Corporation Limited", "bank", 0.50},
	}
	for _, values := range institutions {
		if _, err := a.db.Exec(ctx, `
			INSERT INTO institutions(code,name,type,threshold) VALUES ($1,$2,$3,$4)
			ON CONFLICT (code) DO NOTHING`, values...); err != nil {
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
