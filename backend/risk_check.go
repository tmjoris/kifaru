package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// RiskSignal is the whole answer for one checked artefact. It never names the
// reporting institutions, the reports or their evidence.
type RiskSignal struct {
	Checked      bool   `json:"checked"`
	Flagged      bool   `json:"flagged"`
	Tier         string `json:"tier,omitempty"`
	Institutions int    `json:"institutions"`
}

// riskTierRank orders risk-check tiers from weakest to strongest.
var riskTierRank = map[string]int{
	"reported": 1, "known_risk": 2, alertTierNotice: 3, alertTierReview: 4, alertTierCorroborated: 5,
}

func riskTierForStatus(status string) string {
	switch status {
	case statusCorroborated:
		return alertTierCorroborated
	case statusSingleSourceReview:
		return alertTierReview
	case statusSingleSourceNotice:
		return alertTierNotice
	}
	return "reported"
}

func (signal *RiskSignal) raise(tier string) {
	signal.Flagged = true
	if riskTierRank[tier] > riskTierRank[signal.Tier] {
		signal.Tier = tier
	}
}

// riskCheck lets a member institution ask, before a sign-in or payment,
// whether other institutions hold active reports on a protected device or
// destination account. It answers only flagged, the strongest tier and how
// many institutions reported it; the caller keeps its own decision.
func (a *App) riskCheck(w http.ResponseWriter, r *http.Request) {
	institution, ok := authorizedInstitution(w, r)
	if !ok {
		return
	}
	var request struct {
		DeviceProfile          string `json:"device_profile"`
		DestinationAccountHash string `json:"destination_account_hash"`
		DestinationInstitution string `json:"destination_institution"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid risk check: "+err.Error())
		return
	}
	device := strings.TrimSpace(request.DeviceProfile)
	destination := strings.TrimSpace(request.DestinationAccountHash)
	destinationInstitution := strings.TrimSpace(request.DestinationInstitution)
	if device == "" && destination == "" {
		writeError(w, http.StatusUnprocessableEntity, "device_profile or destination_account_hash is required")
		return
	}
	for name, value := range map[string]string{"device_profile": device, "destination_account_hash": destination} {
		if value != "" && !protectedIdentifierPattern.MatchString(value) {
			writeError(w, http.StatusUnprocessableEntity, name+" must use a protected sha256 digest")
			return
		}
	}
	ctx := r.Context()
	if destination != "" {
		var exists bool
		if err := a.db.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM institutions WHERE code=$1 AND active=1)",
			destinationInstitution).Scan(&exists); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !exists {
			writeError(w, http.StatusUnprocessableEntity,
				fmt.Sprintf("unknown destination institution %q", destinationInstitution))
			return
		}
	}

	result := map[string]*RiskSignal{
		"device_profile":      {Checked: device != ""},
		"destination_account": {Checked: destination != ""},
	}
	// The same filters as corroboration: active, unexpired, qualifying reports
	// from another institution within the matching window.
	since := time.Now().UTC().Add(-30 * 24 * time.Hour).Format("2006-01-02T15:04:05Z")
	rows, err := a.db.Query(ctx, `SELECT a.artefact_type,v.status,COUNT(DISTINCT a.institution_code)
		FROM artefacts a
		JOIN reports r ON r.report_id=a.report_id
		JOIN validations v ON v.report_id=a.report_id AND v.is_current=1
		WHERE a.institution_code != $1
		  AND r.lifecycle_state=$2
		  AND r.expires_at>NOW()
		  AND v.status = ANY($3)
		  AND a.observed_at >= $4
		  AND (
		    (a.artefact_type='device_profile' AND $5 != '' AND a.artefact_hash=$5 AND a.match_scope='') OR
		    (a.artefact_type='destination_account' AND $6 != '' AND a.artefact_hash=$6 AND a.match_scope=$7)
		  )
		GROUP BY a.artefact_type,v.status`,
		institution, lifecycleActive, qualifyingStatuses, since, device, destination, destinationInstitution)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	institutions := map[string]int{}
	for rows.Next() {
		var artefactType, status string
		var count int
		if err := rows.Scan(&artefactType, &status, &count); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		result[artefactType].raise(riskTierForStatus(status))
		institutions[artefactType] = max(institutions[artefactType], count)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for artefactType, count := range institutions {
		result[artefactType].Institutions = count
	}
	if destination != "" {
		kb, err := kbListsFrom(ctx, a.db)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// A known-good destination is never flagged, matching alert policy.
		if kb["known_good"][destination] {
			result["destination_account"] = &RiskSignal{Checked: true}
		} else if kb["known_bad"][destination] {
			result["destination_account"].raise("known_risk")
		}
	}

	user := authUserFromContext(ctx)
	if err := a.audit(ctx, user.Email, "risk.check", institution, fmt.Sprintf("device=%t destination=%t",
		result["device_profile"].Flagged, result["destination_account"].Flagged)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"institution": institution, "checked_at": utcNow(), "policy_version": policyVersion,
		"device": result["device_profile"], "destination": result["destination_account"],
	})
}
