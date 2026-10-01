package main

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var userAliasPattern = regexp.MustCompile(`^[a-z][a-z0-9]{2,31}$`)

type UserAccessRequest struct {
	RequestID           string `json:"request_id"`
	InstitutionCode     string `json:"institution_code"`
	InstitutionName     string `json:"institution_name"`
	Alias               string `json:"alias"`
	Email               string `json:"email"`
	Status              string `json:"status"`
	RequestedByEmail    string `json:"requested_by_email"`
	RequestedAt         string `json:"requested_at"`
	ReviewedByEmail     string `json:"reviewed_by_email"`
	ReviewedAt          string `json:"reviewed_at"`
	ReviewNote          string `json:"review_note"`
	AdmittedUserID      string `json:"admitted_user_id"`
	ApprovedPasswordTip string `json:"approved_password_tip"`
}

func institutionEmailDomain(code string) (string, bool) {
	for _, institution := range kenyanBanks {
		if institution.Code == code {
			return strings.ToLower(institution.Ref) + ".co.ke", true
		}
	}
	return "", false
}

func normalizeUserAlias(value string) (string, error) {
	alias := strings.ToLower(strings.TrimSpace(value))
	if !userAliasPattern.MatchString(alias) {
		return "", errors.New("alias must be 3-32 lowercase letters or numbers and start with a letter")
	}
	return alias, nil
}

func displayNameFromAlias(alias string) string {
	if alias == "" {
		return ""
	}
	return strings.ToUpper(alias[:1]) + alias[1:]
}

func isUniqueViolation(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505"
}

func (a *App) userAccessRequests(w http.ResponseWriter, r *http.Request) {
	user := authUserFromContext(r.Context())
	switch r.Method {
	case http.MethodGet:
		a.listUserAccessRequests(w, r, user)
	case http.MethodPost:
		if !requireRole(w, user, "institution") {
			return
		}
		a.createUserAccessRequest(w, r, user)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) listUserAccessRequests(w http.ResponseWriter, r *http.Request, user AuthUser) {
	query := `SELECT r.request_id,r.institution_code,i.name,r.alias,r.email,r.status,
		requester.email,r.requested_at,COALESCE(reviewer.email,''),
		r.reviewed_at,r.review_note,COALESCE(r.admitted_user_id,'')
		FROM user_access_requests r
		JOIN institutions i ON i.code=r.institution_code
		JOIN auth_users requester ON requester.user_id=r.requested_by
		LEFT JOIN auth_users reviewer ON reviewer.user_id=r.reviewed_by`
	args := []any{}
	if user.Role == "institution" {
		query += " WHERE r.institution_code=$1"
		args = append(args, user.InstitutionCode)
	}
	query += ` ORDER BY CASE r.status WHEN 'pending' THEN 0 ELSE 1 END,
		r.requested_at DESC`
	rows, err := a.db.Query(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	requests := []UserAccessRequest{}
	for rows.Next() {
		var request UserAccessRequest
		var requestedAt time.Time
		var reviewedAt *time.Time
		if err := rows.Scan(
			&request.RequestID, &request.InstitutionCode, &request.InstitutionName,
			&request.Alias, &request.Email, &request.Status,
			&request.RequestedByEmail, &requestedAt, &request.ReviewedByEmail,
			&reviewedAt, &request.ReviewNote, &request.AdmittedUserID,
		); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		request.RequestedAt = requestedAt.UTC().Format(time.RFC3339)
		if reviewedAt != nil {
			request.ReviewedAt = reviewedAt.UTC().Format(time.RFC3339)
		}
		if request.Status == "approved" {
			request.ApprovedPasswordTip = "Use the shared demo password."
		}
		requests = append(requests, request)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	domain := ""
	if user.Role == "institution" {
		domain, _ = institutionEmailDomain(user.InstitutionCode)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"email_domain": domain,
		"requests":     requests,
	})
}

func (a *App) createUserAccessRequest(w http.ResponseWriter, r *http.Request, user AuthUser) {
	var request struct {
		Alias string `json:"alias"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	alias, err := normalizeUserAlias(request.Alias)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	domain, ok := institutionEmailDomain(user.InstitutionCode)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "the institution has no configured email domain")
		return
	}
	email := alias + "@" + domain

	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	var accountExists bool
	if err := tx.QueryRow(r.Context(),
		"SELECT EXISTS(SELECT 1 FROM auth_users WHERE LOWER(email)=LOWER($1))",
		email).Scan(&accountExists); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if accountExists {
		writeError(w, http.StatusConflict, "that institution email already has an account")
		return
	}

	var existingID, existingStatus string
	err = tx.QueryRow(r.Context(), `SELECT request_id,status
		FROM user_access_requests
		WHERE institution_code=$1 AND alias=$2 FOR UPDATE`,
		user.InstitutionCode, alias).Scan(&existingID, &existingStatus)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		existingID = "uar-" + randomHex(12)
		if _, err := tx.Exec(r.Context(), `INSERT INTO user_access_requests(
			request_id,institution_code,alias,email,status,requested_by,requested_at
		) VALUES ($1,$2,$3,$4,'pending',$5,NOW())`,
			existingID, user.InstitutionCode, alias, email, user.UserID); err != nil {
			if isUniqueViolation(err) {
				writeError(w, http.StatusConflict, "that alias already has a request")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	case existingStatus == "rejected":
		if _, err := tx.Exec(r.Context(), `UPDATE user_access_requests SET
			email=$1,status='pending',requested_by=$2,requested_at=NOW(),
			reviewed_by=NULL,reviewed_at=NULL,review_note='',admitted_user_id=NULL
			WHERE request_id=$3`,
			email, user.UserID, existingID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	case existingStatus == "pending":
		writeError(w, http.StatusConflict, "that alias already has a pending request")
		return
	default:
		writeError(w, http.StatusConflict, "that alias was already admitted")
		return
	}

	newValue := fmt.Sprintf(`{"institution":%q,"alias":%q,"email":%q}`,
		user.InstitutionCode, alias, email)
	if err := auditRecord(r.Context(), tx, user.Email, "user_access.request",
		existingID, "", newValue, "institution user addition request"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"request_id": existingID,
		"alias":      alias,
		"email":      email,
		"status":     "pending",
	})
}

func (a *App) reviewUserAccessRequest(w http.ResponseWriter, r *http.Request, requestID string) {
	user := authUserFromContext(r.Context())
	if !requireRole(w, user, "staff") {
		return
	}
	var request struct {
		Decision string `json:"decision"`
		Note     string `json:"note"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	request.Decision = strings.ToLower(strings.TrimSpace(request.Decision))
	request.Note = strings.TrimSpace(request.Note)
	if request.Decision != "approved" && request.Decision != "rejected" {
		writeError(w, http.StatusUnprocessableEntity, "decision must be approved or rejected")
		return
	}
	if len(request.Note) > 500 {
		writeError(w, http.StatusUnprocessableEntity, "review note must be 500 characters or fewer")
		return
	}

	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	var institutionCode, alias, email, status string
	err = tx.QueryRow(r.Context(), `SELECT institution_code,alias,email,status
		FROM user_access_requests WHERE request_id=$1 FOR UPDATE`,
		requestID).Scan(&institutionCode, &alias, &email, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "unknown user addition request")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if status != "pending" {
		writeError(w, http.StatusConflict, "that request has already been reviewed")
		return
	}

	admittedUserID := ""
	if request.Decision == "approved" {
		var exists bool
		if err := tx.QueryRow(r.Context(),
			"SELECT EXISTS(SELECT 1 FROM auth_users WHERE LOWER(email)=LOWER($1))",
			email).Scan(&exists); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if exists {
			writeError(w, http.StatusConflict, "that institution email already has an account")
			return
		}
		admittedUserID = "admitted-" + requestID
		if _, err := tx.Exec(r.Context(), `INSERT INTO auth_users(
			user_id,email,display_name,password_hash,role,institution_code
		) VALUES ($1,$2,$3,$4,'institution',$5)`,
			admittedUserID, email, displayNameFromAlias(alias),
			demoPasswordHash, institutionCode); err != nil {
			if isUniqueViolation(err) {
				writeError(w, http.StatusConflict, "that institution email already has an account")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	if _, err := tx.Exec(r.Context(), `UPDATE user_access_requests SET
		status=$1,reviewed_by=$2,reviewed_at=NOW(),review_note=$3,
		admitted_user_id=NULLIF($4,'')
		WHERE request_id=$5`,
		request.Decision, user.UserID, request.Note, admittedUserID, requestID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	newValue := fmt.Sprintf(`{"decision":%q,"email":%q,"note":%q}`,
		request.Decision, email, request.Note)
	if err := auditRecord(r.Context(), tx, user.Email, "user_access.review",
		requestID, `{"status":"pending"}`, newValue, "staff user addition review"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"request_id": requestID,
		"status":     request.Decision,
		"email":      email,
	})
}
