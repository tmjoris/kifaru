package main

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"time"
)

// ingestSlots bounds how many report submissions are worked on at once.
//
// On a tenth of a CPU, taking on more work than can be finished makes every
// request slower until they all time out: measured, offering 100 reports a
// second collapsed throughput to 27 a second. Turning the excess away at once
// with 503 and Retry-After keeps throughput at the ceiling and lets a bank's
// connector retry shortly, which a well-behaved connector does anyway.
var ingestSlots = make(chan struct{}, envInt("INGEST_CONCURRENCY", 64))

func admitIngest(w http.ResponseWriter) (release func(), ok bool) {
	select {
	case ingestSlots <- struct{}{}:
		return func() { <-ingestSlots }, true
	default:
		w.Header().Set("Retry-After", "2")
		writeError(w, http.StatusServiceUnavailable, "the service is at capacity, retry shortly")
		return nil, false
	}
}

// ---- retention ----------------------------------------------------------------------------

// retentionDays is how long past its expiry a report is kept. Zero (the default)
// keeps everything. Neon's free tier holds 0.5 GB, which is about 400,000
// reports, so a deployment that receives thousands a day needs this on.
func retentionDays() int {
	days, _ := strconv.Atoi(envString("RETENTION_DAYS", "0"))
	return days
}

const pruneBatch = 2000

// pruneOnce deletes one batch of reports that expired more than the retention
// period ago, with everything that hangs off them, and returns how many it
// removed. Seeded demo history and the attack scenario are left to their own
// reset buttons. It deletes only in small batches so it never holds locks long.
func (a *App) pruneOnce(ctx context.Context, days int) (int, error) {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var ids []string
	rows, err := tx.Query(ctx, `SELECT report_id FROM reports
		WHERE expires_at < NOW() - make_interval(days => $1::int)
		  AND COALESCE(evidence,'') NOT LIKE '%"seeded_history":true%'
		  AND COALESCE(evidence,'') NOT LIKE '%"attack_scenario":%'
		ORDER BY expires_at LIMIT $2`, days, pruneBatch)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return 0, err
	}
	for _, statement := range []string{
		`DELETE FROM alerts WHERE report_id = ANY($1)`, // alert_actions cascade
		`DELETE FROM validations WHERE report_id = ANY($1)`,
		`DELETE FROM reports WHERE report_id = ANY($1)`, // artefacts cascade
	} {
		if _, err := tx.Exec(ctx, statement, ids); err != nil {
			return 0, err
		}
	}
	return len(ids), tx.Commit(ctx)
}

// runRetention prunes once an hour when RETENTION_DAYS is set, and trims the
// audit log and read notifications on the same schedule.
func (a *App) runRetention(ctx context.Context) {
	days := retentionDays()
	if days <= 0 {
		return
	}
	log.Printf("retention on: pruning reports expired more than %d days ago", days)
	for {
		for {
			pruneCtx, cancel := context.WithTimeout(ctx, time.Minute)
			removed, err := a.pruneOnce(pruneCtx, days)
			cancel()
			if err != nil {
				log.Printf("retention: %v", err)
				break
			}
			if removed < pruneBatch {
				break
			}
			time.Sleep(time.Second) // never monopolise the database
		}
		if _, err := a.db.Exec(ctx, `DELETE FROM audit_log WHERE id IN (
			SELECT id FROM audit_log WHERE at < $1 ORDER BY id LIMIT 5000)`,
			time.Now().UTC().AddDate(0, 0, -(days+30)).Format("2006-01-02T15:04:05Z")); err != nil {
			log.Printf("retention (audit log): %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour):
		}
	}
}
