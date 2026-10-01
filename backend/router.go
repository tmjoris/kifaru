package main

import (
	"net/http"
	"os"
	"strings"
)

func (a *App) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if allowedOrigin(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Add("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Kifaru-CSRF")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func allowedOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	allowed := []string{
		"http://127.0.0.1:5173",
		"http://localhost:5173",
		"https://kifarulive.onrender.com",
	}
	for _, configured := range strings.Split(os.Getenv("FRONTEND_ORIGIN"), ",") {
		if configured = strings.TrimSpace(configured); configured != "" {
			allowed = append(allowed, strings.TrimSuffix(configured, "/"))
		}
	}
	for _, candidate := range allowed {
		if origin == candidate {
			return true
		}
	}
	return false
}

func (a *App) serveHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/":
		writeJSON(w, http.StatusOK, map[string]any{"service": "KIFARU", "health": "/health"})
		return
	case r.Method == http.MethodGet && path == "/health":
		a.health(w, r)
		return
	case r.Method == http.MethodPost && path == "/v1/auth/login":
		a.login(w, r)
		return
	case r.Method == http.MethodGet && path == "/v1/auth/session":
		a.session(w, r)
		return
	}

	authenticatedRequest, user, ok := a.authenticate(w, r)
	if !ok {
		return
	}
	r = authenticatedRequest

	switch {
	case r.Method == http.MethodPost && path == "/v1/auth/logout":
		a.logout(w, r)
	case r.Method == http.MethodGet && path == "/v1/standard":
		writeJSON(w, http.StatusOK, a.standardRaw)
	case r.Method == http.MethodGet && path == "/v1/institutions":
		a.queryRows(w, r, "SELECT * FROM institutions ORDER BY name")
	case path == "/v1/user-requests":
		a.userAccessRequests(w, r)
	case r.Method == http.MethodGet && path == "/v1/stats":
		if !requireRole(w, user, "staff") {
			return
		}
		a.stats(w, r)
	case r.Method == http.MethodGet && path == "/v1/history":
		a.history(w, r)
	case r.Method == http.MethodGet && path == "/v1/reports":
		a.reports(w, r)
	case r.Method == http.MethodGet && path == "/v1/alerts":
		a.alerts(w, r)
	case r.Method == http.MethodGet && path == "/v1/stream":
		a.streamAlerts(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/v1/validations/"):
		a.validationDetail(w, r, strings.TrimPrefix(path, "/v1/validations/"))
	case r.Method == http.MethodPost && path == "/v1/reports":
		a.submitReport(w, r, "rest")
	case r.Method == http.MethodPost && path == "/v1/reports/batch":
		a.submitBatch(w, r)
	case r.Method == http.MethodPost && (path == "/v1/reports/csv" || path == "/validate-csv"):
		a.submitCSV(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/v1/hooks/"):
		channel := "webhook"
		source := strings.TrimPrefix(path, "/v1/hooks/")
		if source == "sentinel" || source == "soc" {
			channel = "soc_connector"
		}
		a.submitReport(w, r, channel)
	case path == "/v1/admin/config":
		a.config(w, r)
	case path == "/v1/admin/kb":
		if r.Method != http.MethodGet && !requireRole(w, user, "staff") {
			return
		}
		a.knowledgeBase(w, r)
	case r.Method == http.MethodPost && path == "/v1/admin/revalidate":
		if !requireRole(w, user, "staff") {
			return
		}
		a.revalidate(w, r)
	case r.Method == http.MethodGet && path == "/v1/admin/audit":
		if !requireRole(w, user, "staff") {
			return
		}
		a.auditLog(w, r)
	case r.Method == http.MethodGet && path == "/v1/admin/demo-stream":
		if !requireRole(w, user, "staff") {
			return
		}
		a.demoStreamStatus(w, r)
	case r.Method == http.MethodPost && path == "/v1/admin/demo-stream/state":
		if !requireRole(w, user, "staff") {
			return
		}
		a.setDemoStreamState(w, r)
	case r.Method == http.MethodPost && path == "/v1/admin/demo-stream/emit":
		if !requireRole(w, user, "staff") {
			return
		}
		a.emitDemoStreamEvent(w, r)
	case r.Method == http.MethodPost && path == "/v1/admin/demo-stream/reset":
		if !requireRole(w, user, "staff") {
			return
		}
		a.resetDemoStream(w, r)
	case r.Method == http.MethodPatch && strings.HasPrefix(path, "/v1/admin/user-requests/"):
		requestID := strings.TrimPrefix(path, "/v1/admin/user-requests/")
		if requestID == "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		a.reviewUserAccessRequest(w, r, requestID)
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/v1/alerts/") && strings.HasSuffix(path, "/state"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/v1/alerts/"), "/state")
		a.setAlertState(w, r, id)
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

func (a *App) health(w http.ResponseWriter, r *http.Request) {
	if err := a.db.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "agent": agentVersion, "standard": a.standard.Version, "db": "postgresql",
	})
}
