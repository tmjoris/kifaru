package main

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

var (
	htmlRef   = regexp.MustCompile(`(?:src|href)="(/static/[^"]+)"`)
	importRef = regexp.MustCompile(`from\s+"(/static/[^"]+)"`)
)

// Smoke tests answer one question fast: does the whole thing start and do the
// main journeys work at all? They use the real embedded pages and the real
// services, started in-process.

func TestSmoke_EveryPageAndItsFilesLoad(t *testing.T) {
	w := newWorld(t)
	pages := []struct {
		name string
		api  api
		path string
	}{
		{"NCBA customer page", w.bank["ncba"], "/"}, {"KCB customer page", w.bank["kcb"], "/"}, {"Equity customer page", w.bank["equity"], "/"},
		{"NCBA operations page", w.bank["ncba"], "/ops"}, {"KCB operations page", w.bank["kcb"], "/ops"}, {"Equity operations page", w.bank["equity"], "/ops"},
		{"director page", w.net, "/"},
	}
	for _, page := range pages {
		t.Run(page.name, func(t *testing.T) {
			status, body, header := page.api.raw("GET", page.path, "", nil)
			if status != http.StatusOK || !strings.HasPrefix(header.Get("Content-Type"), "text/html") {
				t.Fatalf("status %d, content type %q", status, header.Get("Content-Type"))
			}
			if !strings.Contains(string(body), `<div id="app">`) {
				t.Error("the page has no #app root for its script to render into")
			}
			// Every file the page names, and every module those scripts import, must exist.
			queue := []string{}
			for _, match := range htmlRef.FindAllStringSubmatch(string(body), -1) {
				queue = append(queue, match[1])
			}
			if len(queue) < 2 {
				t.Fatalf("the page references %d static files, want its stylesheet and script", len(queue))
			}
			seen := map[string]bool{}
			for len(queue) > 0 {
				path := queue[0]
				queue = queue[1:]
				if seen[path] {
					continue
				}
				seen[path] = true
				status, content, header := page.api.raw("GET", path, "", nil)
				if status != http.StatusOK {
					t.Errorf("%s: status %d", path, status)
					continue
				}
				kind := header.Get("Content-Type")
				if strings.HasSuffix(path, ".js") {
					if !strings.Contains(kind, "javascript") {
						t.Errorf("%s: content type %q, want JavaScript", path, kind)
					}
					for _, match := range importRef.FindAllStringSubmatch(string(content), -1) {
						queue = append(queue, match[1])
					}
				}
				if strings.HasSuffix(path, ".css") && !strings.Contains(kind, "css") {
					t.Errorf("%s: content type %q, want CSS", path, kind)
				}
			}
		})
	}
}

func TestSmoke_ConfigEndpoints(t *testing.T) {
	w := newWorld(t)
	for id, bank := range w.bank {
		status, config := bank.call("GET", "/api/config", "", nil)
		if status != http.StatusOK || config["id"] != id || config["accent"] == "" {
			t.Errorf("%s /api/config: %d %v", id, status, config)
		}
		if customers, _ := config["customers"].([]any); len(customers) == 0 {
			t.Errorf("%s lists no customers to sign in as", id)
		}
		if others, _ := config["banks"].([]any); len(others) != 3 {
			t.Errorf("%s lists %d banks, want 3", id, len(others))
		}
		if status, _ := bank.call("GET", "/api/ops/state", "", nil); status != http.StatusOK {
			t.Errorf("%s /api/ops/state: status %d", id, status)
		}
	}
	_, state := w.net.call("GET", "/api/state", "", nil)
	if banks, _ := state["banks"].([]any); len(banks) != 3 {
		t.Errorf("the director knows %d banks, want 3", len(banks))
	}
	if state["mode"] != "without" || len(state["steps"].([]any)) != 6 {
		t.Errorf("director starts in mode %v with %d steps", state["mode"], len(state["steps"].([]any)))
	}
}

func TestSmoke_CustomerJourney(t *testing.T) {
	w := newWorld(t)
	w.reset("without")
	token := w.bank["ncba"].login("grace", "grace-phone")
	_, me := w.bank["ncba"].call("GET", "/api/me", token, nil)
	if me["name"] != "Grace Achieng" || me["balance"] != 412500.0 {
		t.Fatalf("/api/me = %v", me)
	}
	status, reply := w.bank["ncba"].pay(token, "kcb", "1105108", "Faith Njeri", 5000)
	if status != http.StatusOK || reply["status"] != "completed" {
		t.Fatalf("payment: %d %v", status, reply)
	}
	faith := w.bank["kcb"].login("faith", "faith-phone")
	if got := w.bank["kcb"].balance(faith); got != 535000 {
		t.Errorf("Faith received %.0f, want her 530000 plus the 5000", got)
	}
	if status, _ := w.bank["ncba"].call("POST", "/api/logout", token, nil); status != http.StatusOK {
		t.Errorf("sign out: status %d", status)
	}
}

func TestSmoke_DirectorTakesItsFirstStep(t *testing.T) {
	w := newWorld(t)
	w.reset("without")
	status, state := w.net.call("POST", "/api/autopilot/next", "", map[string]any{})
	if status != http.StatusOK || state["step"] != 1.0 {
		t.Fatalf("first step: %d %v", status, state)
	}
	if log, _ := state["attacker"].([]any); len(log) == 0 {
		t.Error("the attacker console should show what the first step did")
	}
	if got := w.balance("equity", "kiprono", "checker"); got != 85000 {
		t.Errorf("the mule account holds %.0f after step 1, want 85000", got)
	}
}

func TestSmoke_OpsActions(t *testing.T) {
	w := newWorld(t)
	for _, action := range []struct {
		path string
		body any
	}{
		{"/api/ops/connector", map[string]bool{"enabled": false}},
		{"/api/ops/hold", map[string]string{"account": "0114417"}},
		{"/api/ops/return", map[string]string{"account": "0114417"}},
	} {
		if status, reply := w.bank["equity"].call("POST", action.path, "", action.body); status != http.StatusOK {
			t.Errorf("%s: %d %v", action.path, status, reply)
		}
	}
}
