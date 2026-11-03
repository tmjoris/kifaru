package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// Trends are counted in Kenyan time, so "today" matches the analyst's day.
var trendZone = time.FixedZone("EAT", 3*60*60)

const (
	trendDefaultDays = 30
	trendMaxDays     = 90
	seedHistoryDays  = 60
	seedHistoryActor = "seeded-history"
	seedHistoryLabel = "seeded history"
)

// The banks the seeded history moves money between: the three demo banks
// (NCBA, KCB and Equity) and three more, so the ranking has a spread.
var seedHistoryInstitutions = []string{
	"bank_a", "bank_b", "psp_c", "sacco_d", "ke:co-operative-bank-of-kenya", "ke:safaricom-m-pesa",
}

type seedHistoryKey struct{}

// fromSeedHistory reports whether a report is being written by the history
// seed, the only caller allowed to set the seeded_history field.
func fromSeedHistory(ctx context.Context) bool {
	marked, _ := ctx.Value(seedHistoryKey{}).(bool)
	return marked
}

const seededEvidence = `%"seeded_history":true%`

type trendEvent struct {
	kind        string
	at          time.Time
	institution string
	tier        string
	outcome     string
	decision    string
	destination string
}

type trendCounts struct {
	Reports            int `json:"reports"`
	Alerts             int `json:"alerts"`
	Corroborated       int `json:"corroborated"`
	SingleSourceReview int `json:"single_source_review"`
	SingleSourceNotice int `json:"single_source_notice"`
	Held               int `json:"held"`
	Released           int `json:"released"`
	Recovered          int `json:"recovered"`
	Blocked            int `json:"blocked"`
	Allowed            int `json:"allowed"`
	Incidents          int `json:"incidents"`
}

// trends answers GET /v1/stats/trends. An institution user only ever sees
// its own institution; staff see the whole ecosystem or one institution.
func (a *App) trends(w http.ResponseWriter, r *http.Request) {
	user := authUserFromContext(r.Context())
	institution := strings.TrimSpace(r.URL.Query().Get("institution"))
	if user.Role != "staff" {
		if institution != "" && institution != user.InstitutionCode {
			writeError(w, http.StatusForbidden, "you can only view your own institution's trends")
			return
		}
		institution = user.InstitutionCode
	}
	if institution == "*" {
		institution = ""
	}
	days := trendDefaultDays
	if raw := r.URL.Query().Get("days"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > trendMaxDays {
			writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf("days must be between 1 and %d", trendMaxDays))
			return
		}
		days = parsed
	}
	out, err := a.trendSummary(r.Context(), institution, days, time.Now())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func dayStart(t time.Time) time.Time {
	local := t.In(trendZone)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, trendZone)
}

func (a *App) trendSummary(ctx context.Context, institution string, days int, now time.Time) (map[string]any, error) {
	today := dayStart(now)
	monthStart := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, trendZone)
	lastMonthStart := monthStart.AddDate(0, -1, 0)
	seriesStart := today.AddDate(0, 0, -(days - 1))
	from := lastMonthStart.AddDate(0, 0, -1)
	if seriesStart.Before(from) {
		from = seriesStart
	}
	events, err := trendEvents(ctx, a.db, institution, from)
	if err != nil {
		return nil, err
	}

	// This month so far is compared with the same days of last month, so the
	// comparison stays fair early in the month.
	elapsed := now.In(trendZone).Sub(monthStart)
	lastMonthEnd := lastMonthStart.Add(elapsed)
	if next := lastMonthStart.AddDate(0, 1, 0); lastMonthEnd.After(next) {
		lastMonthEnd = next
	}
	periods := map[string][2]time.Time{
		"today":      {today, today.AddDate(0, 0, 1)},
		"yesterday":  {today.AddDate(0, 0, -1), today},
		"this_month": {monthStart, today.AddDate(0, 0, 1)},
		"last_month": {lastMonthStart, lastMonthEnd},
	}
	comparison := map[string]trendCounts{}
	for name, span := range periods {
		comparison[name] = countEvents(events, span[0], span[1])
	}

	series := make([]map[string]any, 0, days)
	for day := seriesStart; !day.After(today); day = day.AddDate(0, 0, 1) {
		counts := countEvents(events, day, day.AddDate(0, 0, 1))
		series = append(series, map[string]any{
			"day": day.Format("2006-01-02"), "reports": counts.Reports, "alerts": counts.Alerts,
			"blocked": counts.Blocked, "released": counts.Released,
		})
	}

	var seeded int
	if err := a.db.QueryRow(ctx, `SELECT COUNT(*) FROM reports
		WHERE evidence LIKE $1 AND ($2='' OR reporting_institution=$2 OR destination_institution=$2)`,
		seededEvidence, institution).Scan(&seeded); err != nil {
		return nil, err
	}

	out := map[string]any{
		"scope": "ecosystem", "institution": institution, "days": days,
		"timezone": "Africa/Nairobi", "today": today.Format("2006-01-02"),
		"periods": map[string]string{
			"this_month": monthStart.Format("2006-01-02"),
			"last_month": lastMonthStart.Format("2006-01-02") + " to " + lastMonthEnd.Add(-time.Second).Format("2006-01-02"),
		},
		"comparison": comparison, "series": series, "seeded_reports": seeded,
	}
	if institution != "" {
		out["scope"] = "institution"
		return out, nil
	}
	ranking, err := trendRanking(ctx, a.db, seriesStart)
	if err != nil {
		return nil, err
	}
	out["ranking"] = ranking
	return out, nil
}

// trendEvents reads reports, alerts and receiver outcomes since from. Reports
// count for the institution that sent them; alerts and outcomes count for the
// institution that received them.
func trendEvents(ctx context.Context, store dbRunner, institution string, from time.Time) ([]trendEvent, error) {
	const isTimestamp = `~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T'`
	rows, err := store.Query(ctx, `
		SELECT 'report', submitted_at::timestamptz, reporting_institution, '', '',
			CASE WHEN evidence LIKE '%"bank_decision":"blocked"%' THEN 'blocked'
			     WHEN evidence LIKE '%"bank_decision":"allowed"%' THEN 'allowed' ELSE '' END, ''
		FROM reports
		WHERE submitted_at `+isTimestamp+` AND submitted_at::timestamptz >= $1
		  AND ($2='' OR reporting_institution=$2)
		UNION ALL
		SELECT 'alert', issued_at::timestamptz, receiving_institution, alert_tier, '', '',
			COALESCE(destination_account_hash,'')
		FROM alerts
		WHERE issued_at `+isTimestamp+` AND issued_at::timestamptz >= $1
		  AND ($2='' OR receiving_institution=$2)
		UNION ALL
		SELECT 'outcome', updated_at, receiving_institution, alert_tier, outcome, '', ''
		FROM alerts
		WHERE outcome<>'' AND updated_at >= $1 AND ($2='' OR receiving_institution=$2)`,
		from, institution)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []trendEvent{}
	for rows.Next() {
		var event trendEvent
		if err := rows.Scan(&event.kind, &event.at, &event.institution, &event.tier,
			&event.outcome, &event.decision, &event.destination); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func countEvents(events []trendEvent, from, to time.Time) trendCounts {
	var counts trendCounts
	incidents := map[string]bool{}
	for _, event := range events {
		if event.at.Before(from) || !event.at.Before(to) {
			continue
		}
		switch event.kind {
		case "report":
			counts.Reports++
			switch event.decision {
			case "blocked":
				counts.Blocked++
			case "allowed":
				counts.Allowed++
			}
		case "alert":
			counts.Alerts++
			switch event.tier {
			case alertTierCorroborated:
				counts.Corroborated++
				if event.destination != "" {
					incidents[event.destination] = true
				}
			case alertTierReview:
				counts.SingleSourceReview++
			case alertTierNotice:
				counts.SingleSourceNotice++
			}
		case "outcome":
			switch event.outcome {
			case "held":
				counts.Held++
				counts.Blocked++
			case "recovered":
				counts.Recovered++
				counts.Blocked++
			case "released":
				counts.Released++
				counts.Allowed++
			}
		}
	}
	counts.Incidents = len(incidents)
	return counts
}

func trendRanking(ctx context.Context, store dbRunner, from time.Time) ([]map[string]any, error) {
	rows, err := rowsFrom(ctx, store, `
		SELECT i.code AS institution, i.name,
			(SELECT COUNT(*) FROM reports r WHERE r.reporting_institution=i.code
				AND r.submitted_at ~ '^[0-9]{4}-' AND r.submitted_at::timestamptz >= $1) AS reports,
			(SELECT COUNT(*) FROM alerts al WHERE al.receiving_institution=i.code
				AND al.issued_at ~ '^[0-9]{4}-' AND al.issued_at::timestamptz >= $1) AS alerts
		FROM institutions i`, from)
	if err != nil {
		return nil, err
	}
	ranking := []map[string]any{}
	for _, row := range rows {
		reports, _ := numberValue(row["reports"])
		alerts, _ := numberValue(row["alerts"])
		if reports+alerts == 0 {
			continue
		}
		ranking = append(ranking, map[string]any{
			"institution": row["institution"], "name": row["name"],
			"reports": int(reports), "alerts": int(alerts),
		})
	}
	sort.SliceStable(ranking, func(i, j int) bool {
		left := ranking[i]["reports"].(int) + ranking[i]["alerts"].(int)
		right := ranking[j]["reports"].(int) + ranking[j]["alerts"].(int)
		if left != right {
			return left > right
		}
		return fmt.Sprint(ranking[i]["name"]) < fmt.Sprint(ranking[j]["name"])
	})
	if len(ranking) > 10 {
		ranking = ranking[:10]
	}
	return ranking, nil
}

// historyJob tracks the history seed, which runs in the background: on a
// hosted database it takes longer than a proxy keeps a request open.
type historyJob struct {
	mu      sync.Mutex
	running bool
	last    map[string]any
	err     string
}

func (job *historyJob) snapshot() map[string]any {
	job.mu.Lock()
	defer job.mu.Unlock()
	return map[string]any{"running": job.running, "last": job.last, "error": job.err}
}

// seedTrendHistory answers POST /v1/admin/trend-history/seed. It starts
// replacing any earlier seeded history with about 60 days of synthetic
// reports, alerts and receiver outcomes, written through the normal pipeline
// and then moved back to the day they stand for. GET
// /v1/admin/trend-history reports when it has finished.
func (a *App) seedTrendHistory(w http.ResponseWriter, r *http.Request) {
	a.seedJob.mu.Lock()
	if a.seedJob.running {
		a.seedJob.mu.Unlock()
		writeError(w, http.StatusConflict, "seeded history is already being written")
		return
	}
	a.seedJob.running, a.seedJob.err = true, ""
	a.seedJob.mu.Unlock()

	// The seed outlives the request, but keeps its signed-in user for the audit.
	ctx := context.WithoutCancel(r.Context())
	go func() {
		summary, err := a.runTrendSeed(ctx)
		a.seedJob.mu.Lock()
		defer a.seedJob.mu.Unlock()
		a.seedJob.running = false
		if err != nil {
			a.seedJob.err = err.Error()
			return
		}
		a.seedJob.last = summary
	}()
	writeJSON(w, http.StatusAccepted, a.seedJob.snapshot())
}

// trendHistoryStatus answers GET /v1/admin/trend-history.
func (a *App) trendHistoryStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.seedJob.snapshot())
}

func (a *App) runTrendSeed(ctx context.Context) (map[string]any, error) {
	removed, err := a.clearTrendHistory(ctx)
	if err != nil {
		return nil, err
	}
	summary, err := a.writeTrendHistory(ctx, time.Now(), seedHistoryDays)
	if err != nil {
		return nil, err
	}
	summary["removed_reports"] = removed
	if err := a.audit(ctx, authUserFromContext(ctx).Email, "trend_history.seeded", "",
		fmt.Sprintf(`{"reports":%d,"alerts":%d,"outcomes":%d}`,
			summary["reports"], summary["alerts"], summary["outcomes"])); err != nil {
		return nil, err
	}
	a.publishStreamEvent([]string{"*"}, "demo-event", "trend-history", map[string]any{"trend_history": "seeded"})
	return summary, nil
}

// resetTrendHistory answers POST /v1/admin/trend-history/reset.
func (a *App) resetTrendHistory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if a.seedJob.snapshot()["running"] == true {
		writeError(w, http.StatusConflict, "wait for the seeded history to finish first")
		return
	}
	removed, err := a.clearTrendHistory(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := a.audit(ctx, authUserFromContext(ctx).Email, "trend_history.reset", "",
		fmt.Sprintf(`{"removed_reports":%d}`, removed)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.publishStreamEvent([]string{"*"}, "demo-event", "trend-history", map[string]any{"trend_history": "reset"})
	writeJSON(w, http.StatusOK, map[string]any{"removed_reports": removed})
}

func (a *App) clearTrendHistory(ctx context.Context) (int64, error) {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	seeded := `SELECT report_id FROM reports WHERE evidence LIKE $1`
	queries := []string{
		`DELETE FROM notifications WHERE record_id IN (
			SELECT alert_id FROM alerts WHERE report_id IN (` + seeded + `))`,
		`DELETE FROM alert_actions WHERE alert_id IN (
			SELECT alert_id FROM alerts WHERE report_id IN (` + seeded + `))`,
		`DELETE FROM alerts WHERE report_id IN (` + seeded + `)`,
		`DELETE FROM validations WHERE report_id IN (` + seeded + `)`,
		`DELETE FROM artefacts WHERE report_id IN (` + seeded + `)`,
	}
	for _, query := range queries {
		if _, err := tx.Exec(ctx, query, seededEvidence); err != nil {
			return 0, err
		}
	}
	tag, err := tx.Exec(ctx, `DELETE FROM reports WHERE evidence LIKE $1`, seededEvidence)
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM knowledge_base WHERE label=$1`, seedHistoryLabel); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), tx.Commit(ctx)
}

// seedIncident is one synthetic case in the history: a mule account that one
// or two banks report, and what the receiving bank did about it.
type seedIncident struct {
	at        time.Time
	kind      string // pair, review, notice or awaiting
	reporters []string
	receiver  string
	outcome   string // held, recovered, released or ""
}

func planTrendHistory(now time.Time, days int) []seedIncident {
	random := rand.New(rand.NewPCG(2026, 3))
	today := dayStart(now)
	incidents := []seedIncident{}
	for back := days; back >= 0; back-- {
		day := today.AddDate(0, 0, -back)
		// A slow upward trend, quieter weekends and some day-to-day noise.
		count := 2 + (days-back)/20 + random.IntN(3)
		if weekday := day.Weekday(); weekday == time.Saturday || weekday == time.Sunday {
			count = max(1, count-2)
		}
		for n := 0; n < count; n++ {
			at := day.Add(time.Duration(7+random.IntN(13))*time.Hour + time.Duration(random.IntN(60))*time.Minute)
			if back == 0 && at.After(now) {
				continue
			}
			receiver := seedHistoryInstitutions[random.IntN(len(seedHistoryInstitutions))]
			others := []string{}
			for _, code := range seedHistoryInstitutions {
				if code != receiver {
					others = append(others, code)
				}
			}
			random.Shuffle(len(others), func(i, j int) { others[i], others[j] = others[j], others[i] })
			incident := seedIncident{at: at, receiver: receiver, reporters: others[:1]}
			switch roll := random.IntN(100); {
			case roll < 40:
				incident.kind = "pair"
				incident.reporters = others[:2]
			case roll < 65:
				incident.kind = "review"
			case roll < 75:
				incident.kind = "notice"
			default:
				incident.kind = "awaiting"
			}
			if incident.kind != "awaiting" {
				switch roll := random.IntN(100); {
				case roll < 30:
					incident.outcome = "held"
				case roll < 55:
					incident.outcome = "recovered"
				case roll < 75:
					incident.outcome = "released"
				}
			}
			incidents = append(incidents, incident)
		}
	}
	sort.SliceStable(incidents, func(i, j int) bool { return incidents[i].at.Before(incidents[j].at) })
	return incidents
}

func seedReport(run string, sequence int, incident seedIncident, reporter string, second bool) ReportIn {
	mule := demoHash(fmt.Sprintf("seeded-mule:%s:%d", run, sequence))
	evidence := map[string]any{
		"seeded_history":      true,
		"is_new_device":       true,
		"is_new_beneficiary":  true,
		"account_age_days":    3 + sequence%20,
		"distinct_senders_7d": 2 + sequence%5,
		"device_profile":      demoHash(fmt.Sprintf("seeded-device:%s:%d:%s", run, sequence, reporter)),
		"bank_decision":       "blocked",
	}
	score := 0.84
	if incident.kind == "review" || (incident.kind == "pair" && !second) {
		// The customer confirmed a completed transfer, so the money moved.
		evidence["victim_confirmed"] = true
		evidence["synthetic_bank_demo"] = true
		evidence["bank_decision"] = "allowed"
		score = 0.62
	}
	return ReportIn{
		ReportingInstitution:   reporter,
		ReportingSystem:        "Seeded history",
		TransactionRef:         fmt.Sprintf("SEED-%s-%04d-%s", run, sequence, reporter),
		TransactionTimestamp:   incident.at.UTC().Format(time.RFC3339),
		SubjectAccountHash:     demoHash(fmt.Sprintf("seeded-victim-account:%s:%d:%s", run, sequence, reporter)),
		SubjectCustomerHash:    demoHash(fmt.Sprintf("seeded-victim:%s:%d:%s", run, sequence, reporter)),
		DestinationAccountHash: mule,
		DestinationInstitution: incident.receiver,
		Amount:                 float64(15000 + (sequence*7919)%180000),
		Currency:               "KES",
		Channel:                "instant_payment",
		BankRiskScore:          score,
		RiskCodes:              []string{"ATO-460", "MUL-440"},
		Evidence:               evidence,
		Narrative:              "Seeded history: synthetic account takeover into a mule account.",
	}
}

// writeTrendHistory runs every planned incident through the pipeline in time
// order, records the receivers' decisions, and then dates each record to the
// moment it stands for.
func (a *App) writeTrendHistory(ctx context.Context, now time.Time, days int) (map[string]any, error) {
	run := randomHex(3)
	seedCtx := context.WithValue(ctx, seedHistoryKey{}, true)
	reportTimes := map[string]time.Time{}
	alertTimes := map[string]time.Time{}
	outcomeTimes := map[string]time.Time{}
	outcomes := 0
	for sequence, incident := range planTrendHistory(now, days) {
		if incident.kind == "notice" {
			mule := demoHash(fmt.Sprintf("seeded-mule:%s:%d", run, sequence))
			if err := kbAddWith(ctx, a.db, mule, "known_bad", seedHistoryLabel, seedHistoryActor); err != nil {
				return nil, err
			}
		}
		for index, reporter := range incident.reporters {
			at := incident.at.Add(time.Duration(index*47) * time.Minute)
			result, apiErr := a.process(seedCtx, seedReport(run, sequence, incident, reporter, index > 0), "batch")
			if apiErr != nil {
				return nil, errors.New(apiErr.message)
			}
			reportTimes[result["report_id"].(string)] = at
			rows, err := a.rows(ctx, `SELECT a.alert_id FROM alerts a JOIN reports r ON r.report_id=a.report_id
				WHERE r.evidence LIKE $1 AND r.destination_account_hash=$2`, seededEvidence,
				demoHash(fmt.Sprintf("seeded-mule:%s:%d", run, sequence)))
			if err != nil {
				return nil, err
			}
			for _, row := range rows {
				alertID := fmt.Sprint(row["alert_id"])
				if _, seen := alertTimes[alertID]; !seen {
					alertTimes[alertID] = at
				}
			}
		}
		if incident.outcome == "" {
			continue
		}
		var alertID string
		err := a.db.QueryRow(ctx, `SELECT a.alert_id FROM alerts a JOIN reports r ON r.report_id=a.report_id
			WHERE r.destination_account_hash=$1 AND a.receiving_institution=$2
			ORDER BY a.alert_id LIMIT 1`,
			demoHash(fmt.Sprintf("seeded-mule:%s:%d", run, sequence)), incident.receiver).Scan(&alertID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := a.seedReceiverActs(ctx, alertID, incident.receiver, incident.outcome); err != nil {
			return nil, err
		}
		outcomeTimes[alertID] = incident.at.Add(time.Duration(2+sequence%10) * time.Hour)
		outcomes++
	}

	tx, err := a.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The seeded decisions are history, not news, so they do not fill the
	// reporting banks' notification lists.
	if _, err := tx.Exec(ctx, `DELETE FROM notifications WHERE record_id IN (
		SELECT a.alert_id FROM alerts a JOIN reports r ON r.report_id=a.report_id
		WHERE r.evidence LIKE $1)`, seededEvidence); err != nil {
		return nil, err
	}
	for reportID, at := range reportTimes {
		stamp := at.UTC().Format("2006-01-02T15:04:05Z")
		if _, err := tx.Exec(ctx, `UPDATE reports SET submitted_at=$2 WHERE report_id=$1`, reportID, stamp); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE validations SET validated_at=$2 WHERE report_id=$1`, reportID, stamp); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE artefacts SET observed_at=$2 WHERE report_id=$1`, reportID, stamp); err != nil {
			return nil, err
		}
	}
	for alertID, at := range alertTimes {
		updated, decided := outcomeTimes[alertID]
		if !decided {
			updated = at
		}
		if _, err := tx.Exec(ctx, `UPDATE alerts SET issued_at=$2,updated_at=$3 WHERE alert_id=$1`,
			alertID, at.UTC().Format("2006-01-02T15:04:05Z"), updated); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{
		"run": run, "days": days, "reports": len(reportTimes), "alerts": len(alertTimes), "outcomes": outcomes,
	}, nil
}

// seedReceiverActs records a receiving bank's decision on a seeded alert. The
// actor is the seed, never a real analyst, and reporter standing ignores it.
func (a *App) seedReceiverActs(ctx context.Context, alertID, receiver, outcome string) error {
	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	user := AuthUser{Email: seedHistoryActor, Role: "institution", InstitutionCode: receiver}
	transitions := [][2]string{{"acknowledged", ""}}
	if outcome == "recovered" {
		transitions = append(transitions, [2]string{"actioned", "held"})
	}
	transitions = append(transitions, [2]string{"actioned", outcome})
	comment := "Seeded history: synthetic receiver decision."
	for _, transition := range transitions {
		if _, _, apiErr := a.applyAlertTransition(ctx, tx, alertID, transition[0], transition[1], comment, user); apiErr != nil {
			return errors.New(apiErr.message)
		}
	}
	return tx.Commit(ctx)
}
