package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

const (
	authCookieName       = "kifaru_session"
	demoPasswordHash     = "$2a$12$S1DbxRH5XaRj/ebH8gEFNuZQ8DYDJ6fypRSSHV5Js8rDOmsbxU2q6"
	standardSessionTTL   = 8 * time.Hour
	persistentSessionTTL = 7 * 24 * time.Hour
)

type authContextKey struct{}

type AuthUser struct {
	UserID          string
	Email           string
	DisplayName     string
	Role            string
	InstitutionCode string
	InstitutionName string
	CSRFToken       string
	ExpiresAt       time.Time
}

type authSessionPayload struct {
	Email           string `json:"email"`
	DisplayName     string `json:"display_name"`
	Role            string `json:"role"`
	InstitutionCode string `json:"institution_code"`
	InstitutionName string `json:"institution_name"`
	CSRFToken       string `json:"csrf_token"`
	ExpiresAt       string `json:"expires_at"`
	AccessToken     string `json:"access_token,omitempty"`
}

type demoIdentity struct {
	Key  string
	Name string
}

var repeatingDemoIdentities = []demoIdentity{
	{Key: "john-kamau", Name: "John Kamau"},
	{Key: "mary-wanjiru", Name: "Mary Wanjiru"},
	{Key: "daniel-ouma", Name: "Daniel Ouma"},
	{Key: "sarah-wekesa", Name: "Sarah Wekesa"},
	{Key: "josephine-naliaka", Name: "Josephine Naliaka"},
}

func demoEmailForName(name string, institution Institution) string {
	localPart := strings.ToLower(strings.ReplaceAll(name, " ", ""))
	return localPart + "@" + strings.ToLower(institution.Ref) + ".co.ke"
}

func demoInstitutionEmail(institution Institution) string {
	if institution.DemoEmail != "" {
		return institution.DemoEmail
	}
	return demoEmailForName(institution.DemoName, institution)
}

func demoLoginInstitutions() []Institution {
	return append([]Institution(nil), kenyanBanks...)
}

func (a *App) seedDemoUsers(ctx context.Context) error {
	institutions := demoLoginInstitutions()
	accountsPerInstitution := len(repeatingDemoIdentities) + 1
	seenEmails := make(map[string]string, len(institutions)*accountsPerInstitution)
	for _, institution := range institutions {
		if strings.TrimSpace(institution.DemoName) == "" {
			return fmt.Errorf("demo identity name is missing for %s", institution.Code)
		}
		primaryEmail := demoInstitutionEmail(institution)
		if err := reserveDemoEmail(seenEmails, primaryEmail, institution.Code); err != nil {
			return err
		}
		result, err := a.db.Exec(ctx, `UPDATE auth_users SET
			email=$1,
			display_name=$2,
			password_hash=$3,
			role='institution',
			institution_code=$4,
			active=TRUE,
			updated_at=NOW()
			WHERE role='institution' AND institution_code=$4 AND display_name=$2`,
			primaryEmail, institution.DemoName, demoPasswordHash, institution.Code)
		if err != nil {
			return err
		}
		if result.RowsAffected() > 1 {
			return fmt.Errorf("multiple primary demo users are assigned to %s", institution.Code)
		}
		if result.RowsAffected() == 0 {
			if err := a.upsertDemoInstitutionUser(ctx,
				"demo-"+institution.ID+"-institution-user",
				primaryEmail, institution.DemoName, institution.Code); err != nil {
				return err
			}
		}

		for _, identity := range repeatingDemoIdentities {
			email := demoEmailForName(identity.Name, institution)
			if err := reserveDemoEmail(seenEmails, email, institution.Code); err != nil {
				return err
			}
			if err := a.upsertDemoInstitutionUser(ctx,
				"demo-"+institution.ID+"-"+identity.Key,
				email, identity.Name, institution.Code); err != nil {
				return err
			}
		}
	}
	_, err := a.db.Exec(ctx, `INSERT INTO auth_users(
		user_id,email,display_name,password_hash,role,institution_code
	) VALUES ('demo-kifaru-anthony-jordan','anthonyjordan@kifaru.co.ke',
		'Anthony Jordan',$1,'staff',NULL)
	ON CONFLICT (user_id) DO UPDATE SET
		email=excluded.email,
		display_name=excluded.display_name,
		password_hash=excluded.password_hash,
		role=excluded.role,
		institution_code=excluded.institution_code,
		active=TRUE,
		updated_at=NOW()`, demoPasswordHash)
	return err
}

func reserveDemoEmail(seen map[string]string, email, institutionCode string) error {
	if existing := seen[email]; existing != "" {
		return fmt.Errorf("demo identity email %s is shared by %s and %s",
			email, existing, institutionCode)
	}
	seen[email] = institutionCode
	return nil
}

func (a *App) upsertDemoInstitutionUser(
	ctx context.Context,
	userID, email, displayName, institutionCode string,
) error {
	_, err := a.db.Exec(ctx, `INSERT INTO auth_users(
		user_id,email,display_name,password_hash,role,institution_code
	) VALUES ($1,$2,$3,$4,'institution',$5)
	ON CONFLICT (user_id) DO UPDATE SET
		email=excluded.email,
		display_name=excluded.display_name,
		password_hash=excluded.password_hash,
		role=excluded.role,
		institution_code=excluded.institution_code,
		active=TRUE,
		updated_at=NOW()`,
		userID, email, displayName, demoPasswordHash, institutionCode)
	return err
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Email           string `json:"email"`
		Password        string `json:"password"`
		InstitutionCode string `json:"institution_code"`
		KeepSignedIn    bool   `json:"keep_signed_in"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	email := strings.ToLower(strings.TrimSpace(request.Email))
	if email == "" || request.Password == "" {
		writeError(w, http.StatusUnprocessableEntity, "email and password are required")
		return
	}

	var user AuthUser
	var passwordHash string
	var active bool
	var failedAttempts int
	var lockedUntil *time.Time
	err := a.db.QueryRow(r.Context(), `SELECT u.user_id,u.email,u.display_name,u.password_hash,
		u.role,COALESCE(u.institution_code,''),COALESCE(i.name,''),u.active,
		u.failed_attempts,u.locked_until
		FROM auth_users u
		LEFT JOIN institutions i ON i.code=u.institution_code
		WHERE LOWER(u.email)=LOWER($1)`,
		email).Scan(&user.UserID, &user.Email, &user.DisplayName, &passwordHash,
		&user.Role, &user.InstitutionCode, &user.InstitutionName, &active,
		&failedAttempts, &lockedUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = bcrypt.CompareHashAndPassword([]byte(demoPasswordHash), []byte(request.Password))
		log.Printf("authentication failed for %q", email)
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if lockedUntil != nil && lockedUntil.After(time.Now()) {
		writeError(w, http.StatusTooManyRequests, "account temporarily locked after repeated failed sign-in attempts")
		return
	}
	if !active || bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(request.Password)) != nil {
		if active {
			if _, updateErr := a.db.Exec(r.Context(), `UPDATE auth_users
				SET failed_attempts=failed_attempts+1,
					locked_until=CASE WHEN failed_attempts+1>=5
						THEN NOW()+INTERVAL '5 minutes' ELSE locked_until END,
					updated_at=NOW()
				WHERE user_id=$1`, user.UserID); updateErr != nil {
				writeError(w, http.StatusInternalServerError, updateErr.Error())
				return
			}
		}
		log.Printf("authentication failed for %q", email)
		writeError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}
	if user.Role == "institution" {
		if request.InstitutionCode == "" {
			writeError(w, http.StatusForbidden, "use the institution sign-in for this account")
			return
		}
		if request.InstitutionCode != user.InstitutionCode {
			writeError(w, http.StatusForbidden, "this account is not assigned to the selected institution")
			return
		}
	}
	if user.Role == "staff" && request.InstitutionCode != "" {
		writeError(w, http.StatusForbidden, "use the Kifaru staff sign-in for this account")
		return
	}
	if _, err := a.db.Exec(r.Context(), `UPDATE auth_users
		SET failed_attempts=0,locked_until=NULL,updated_at=NOW() WHERE user_id=$1`,
		user.UserID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	token, err := randomToken(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	csrfToken, err := randomToken(24)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ttl := standardSessionTTL
	if request.KeepSignedIn {
		ttl = persistentSessionTTL
	}
	user.CSRFToken = csrfToken
	user.ExpiresAt = time.Now().UTC().Add(ttl)

	tx, err := a.db.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	if _, err := tx.Exec(r.Context(), "DELETE FROM auth_sessions WHERE expires_at<=NOW()"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := tx.Exec(r.Context(), `INSERT INTO auth_sessions(
		token_hash,csrf_token,user_id,expires_at
	) VALUES ($1,$2,$3,$4)`, hashSessionToken(token), csrfToken, user.UserID, user.ExpiresAt); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := auditRecord(r.Context(), tx, user.Email, "auth.login", user.UserID,
		"", user.Role, "authenticated demo sign-in"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	setAuthCookie(w, r, token, user.ExpiresAt, request.KeepSignedIn)
	payload := authPayload(user)
	payload.AccessToken = token
	writeJSON(w, http.StatusOK, payload)
}

func (a *App) session(w http.ResponseWriter, r *http.Request) {
	user, err := a.authenticatedUser(r)
	if err != nil {
		clearAuthCookie(w, r)
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	writeJSON(w, http.StatusOK, authPayload(user))
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	user := authUserFromContext(r.Context())
	token := sessionToken(r)
	if token != "" {
		tx, beginErr := a.db.Begin(r.Context())
		if beginErr != nil {
			writeError(w, http.StatusInternalServerError, beginErr.Error())
			return
		}
		defer func() { _ = tx.Rollback(r.Context()) }()
		if _, err := tx.Exec(r.Context(), "DELETE FROM auth_sessions WHERE token_hash=$1",
			hashSessionToken(token)); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := auditRecord(r.Context(), tx, user.Email, "auth.logout", user.UserID,
			user.Role, "", "authenticated demo sign-out"); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	clearAuthCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) authenticatedUser(r *http.Request) (AuthUser, error) {
	token := sessionToken(r)
	if token == "" {
		return AuthUser{}, errors.New("session token is missing")
	}
	var user AuthUser
	err := a.db.QueryRow(r.Context(), `SELECT u.user_id,u.email,u.display_name,u.role,
		COALESCE(u.institution_code,''),COALESCE(i.name,''),s.csrf_token,s.expires_at
		FROM auth_sessions s
		JOIN auth_users u ON u.user_id=s.user_id
		LEFT JOIN institutions i ON i.code=u.institution_code
		WHERE s.token_hash=$1 AND s.expires_at>NOW() AND u.active=TRUE`,
		hashSessionToken(token)).Scan(
		&user.UserID, &user.Email, &user.DisplayName, &user.Role,
		&user.InstitutionCode, &user.InstitutionName, &user.CSRFToken, &user.ExpiresAt)
	if err != nil {
		return AuthUser{}, err
	}
	if _, err := a.db.Exec(r.Context(), `UPDATE auth_sessions SET last_seen_at=NOW()
		WHERE token_hash=$1`, hashSessionToken(token)); err != nil {
		return AuthUser{}, err
	}
	return user, nil
}

func (a *App) authenticate(w http.ResponseWriter, r *http.Request) (*http.Request, AuthUser, bool) {
	user, err := a.authenticatedUser(r)
	if err != nil {
		clearAuthCookie(w, r)
		writeError(w, http.StatusUnauthorized, "authentication required")
		return r, AuthUser{}, false
	}
	if isMutation(r.Method) && subtle.ConstantTimeCompare(
		[]byte(r.Header.Get("X-Kifaru-CSRF")), []byte(user.CSRFToken),
	) != 1 {
		writeError(w, http.StatusForbidden, "invalid CSRF token")
		return r, AuthUser{}, false
	}
	r = r.WithContext(context.WithValue(r.Context(), authContextKey{}, user))
	return r, user, true
}

func authUserFromContext(ctx context.Context) AuthUser {
	user, _ := ctx.Value(authContextKey{}).(AuthUser)
	return user
}

func requireRole(w http.ResponseWriter, user AuthUser, role string) bool {
	if user.Role != role {
		writeError(w, http.StatusForbidden, "you do not have permission to use this endpoint")
		return false
	}
	return true
}

func authorizedInstitution(w http.ResponseWriter, r *http.Request) (string, bool) {
	user := authUserFromContext(r.Context())
	institution := strings.TrimSpace(r.URL.Query().Get("institution"))
	if institution == "" {
		writeError(w, http.StatusUnprocessableEntity, "institution is required")
		return "", false
	}
	if user.Role == "institution" && institution != user.InstitutionCode {
		writeError(w, http.StatusForbidden, "you can only access your assigned institution")
		return "", false
	}
	return institution, true
}

func authorizeReport(w http.ResponseWriter, r *http.Request, report *ReportIn) bool {
	user := authUserFromContext(r.Context())
	if user.Role == "staff" {
		return true
	}
	if report.ReportingInstitution == "" {
		report.ReportingInstitution = user.InstitutionCode
	}
	if report.ReportingInstitution != user.InstitutionCode {
		writeError(w, http.StatusForbidden, "reports must belong to your assigned institution")
		return false
	}
	return true
}

func authPayload(user AuthUser) authSessionPayload {
	return authSessionPayload{
		Email: user.Email, DisplayName: user.DisplayName, Role: user.Role,
		InstitutionCode: user.InstitutionCode, InstitutionName: user.InstitutionName,
		CSRFToken: user.CSRFToken, ExpiresAt: user.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func sessionToken(r *http.Request) string {
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(authorization), "bearer ") {
		return strings.TrimSpace(authorization[len("Bearer "):])
	}
	cookie, err := r.Cookie(authCookieName)
	if err == nil {
		return cookie.Value
	}
	return ""
}

func isMutation(method string) bool {
	return method == http.MethodPost || method == http.MethodPatch || method == http.MethodDelete
}

func secureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func setAuthCookie(w http.ResponseWriter, r *http.Request, token string, expiresAt time.Time, persistent bool) {
	secure := secureRequest(r)
	cookie := &http.Cookie{
		Name: authCookieName, Value: token, Path: "/", HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteLaxMode,
	}
	if secure {
		cookie.SameSite = http.SameSiteNoneMode
	}
	if persistent {
		cookie.Expires = expiresAt
		cookie.MaxAge = int(time.Until(expiresAt).Seconds())
	}
	http.SetCookie(w, cookie)
}

func clearAuthCookie(w http.ResponseWriter, r *http.Request) {
	secure := secureRequest(r)
	cookie := &http.Cookie{
		Name: authCookieName, Value: "", Path: "/", HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: -1,
		Expires: time.Unix(1, 0),
	}
	if secure {
		cookie.SameSite = http.SameSiteNoneMode
	}
	http.SetCookie(w, cookie)
}
