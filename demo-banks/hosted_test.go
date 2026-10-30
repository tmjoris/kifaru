package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// hostedWorld runs everything on one port under /demo-banks/, as
// `go run . hosted` does on Render.
func hostedWorld(t *testing.T) (root api, banksByID map[string]*Bank) {
	t.Helper()
	isolate(t)
	server := httptest.NewUnstartedServer(nil)
	internal := "http://" + server.Listener.Addr().String() + hostedPath
	t.Setenv("NETWORK_URL", internal)
	banksByID = map[string]*Bank{}
	services := map[string]http.Handler{}
	for _, cfg := range banks {
		t.Setenv(strings.ToUpper(cfg.ID)+"_URL", internal+"/"+cfg.ID)
		banksByID[cfg.ID] = newBank(cfg)
		services[cfg.ID] = banksByID[cfg.ID].routes()
	}
	internalKey, publicPrefix = "test-key", hostedPath
	t.Cleanup(func() { internalKey, publicPrefix = "", "" })
	server.Config.Handler = hostedRoutes(newNetwork().routes(), services, internalKey)
	server.Start()
	t.Cleanup(server.Close)
	return api{t, server.URL}, banksByID
}

// noRedirects returns the first answer, so the test can see redirects.
var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func TestSmoke_HostedPagesShareOnePort(t *testing.T) {
	root, _ := hostedWorld(t)
	redirects := map[string]string{
		"/":                  hostedPath + "/",
		"/demo-banks":        hostedPath + "/",
		"/demo-banks/ncba":   hostedPath + "/ncba/",
		"/demo-banks/kcb":    hostedPath + "/kcb/",
		"/demo-banks/equity": hostedPath + "/equity/",
	}
	for path, want := range redirects {
		response, err := noRedirects.Get(root.base + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if got := response.Header.Get("Location"); got != want {
			t.Errorf("GET %s redirects to %q, want %q", path, got, want)
		}
	}
	pages := map[string]string{
		"/demo-banks/":                        "static/director.js",
		"/demo-banks/static/style.css":        "",
		"/demo-banks/ncba/":                   "static/customer.js",
		"/demo-banks/kcb/ops":                 "static/ops.js",
		"/demo-banks/equity/static/common.js": "deviceId",
	}
	for path, want := range pages {
		status, body, _ := root.raw("GET", path, "", nil)
		if status != http.StatusOK || !strings.Contains(string(body), want) {
			t.Errorf("GET %s: status %d, want 200 with %q", path, status, want)
		}
	}
	for _, id := range []string{"ncba", "kcb", "equity"} {
		if _, config := root.call("GET", "/demo-banks/"+id+"/api/config", "", nil); config["id"] != id {
			t.Errorf("/demo-banks/%s/api/config answered for %v", id, config["id"])
		}
	}
	_, state := root.call("GET", "/demo-banks/api/state", "", nil)
	links, _ := state["banks"].([]any)
	if len(links) != 3 || links[0].(map[string]any)["url"] != "/demo-banks/ncba" {
		t.Errorf("director bank links = %v, want /demo-banks/<bank>", links)
	}
}

// A payment goes through the switch on the same port and reaches Equity.
func TestSmoke_HostedPaymentCrossesTheSwitch(t *testing.T) {
	root, _ := hostedWorld(t)
	ncba := api{t, root.base + "/demo-banks/ncba"}
	equity := api{t, root.base + "/demo-banks/equity"}
	status, reply := ncba.pay(ncba.login("grace", "grace-phone"), "equity", "0114417", "J. Kiprono", 85000)
	if status != http.StatusOK || reply["status"] != "completed" {
		t.Fatalf("payment: status %d %v", status, reply)
	}
	if got := equity.balance(equity.login("kiprono", "kiprono-phone")); got != 85000 {
		t.Fatalf("Kiprono's balance = %.0f, want 85000", got)
	}
	_, state := root.call("GET", "/demo-banks/api/state", "", nil)
	if entries, _ := state["switch"].([]any); len(entries) != 1 {
		t.Fatalf("switch log = %v, want one settled transfer", state["switch"])
	}
}

// The director's mode buttons reset the banks through the internal route.
func TestSmoke_HostedDirectorResetsTheBanks(t *testing.T) {
	root, hosted := hostedWorld(t)
	status, reply := root.call("POST", "/demo-banks/api/mode", "", map[string]string{"mode": "with"})
	if status != http.StatusOK {
		t.Fatalf("set mode: status %d %v", status, reply)
	}
	for id, bank := range hosted {
		bank.mu.Lock()
		connector := bank.connector
		bank.mu.Unlock()
		if !connector {
			t.Errorf("%s connector is off after With Kifaru", id)
		}
	}
}

// Visitors reach the pages and the bank APIs, but not the routes the
// services use between themselves.
func TestSecurity_HostedInternalRoutesNeedTheKey(t *testing.T) {
	root, _ := hostedWorld(t)
	credit := map[string]any{"ref": "X-1", "from_bank": "ncba", "from_bank_name": "NCBA",
		"from_name": "Someone", "to_bank": "equity", "to_account": "0114417", "amount": 1000000}
	routes := []struct{ method, path string }{
		{"POST", "/demo-banks/switch/transfer"},
		{"POST", "/demo-banks/switch/return"},
		{"POST", "/demo-banks/mail"},
		{"POST", "/demo-banks/equity/interbank/credit"},
		{"POST", "/demo-banks/equity/interbank/return"},
		{"POST", "/demo-banks/ncba/admin/reset"},
	}
	for _, route := range routes {
		if status, _ := root.call(route.method, route.path, "", credit); status != http.StatusForbidden {
			t.Errorf("%s %s without the key: status %d, want 403", route.method, route.path, status)
		}
	}
	equity := api{t, root.base + "/demo-banks/equity"}
	if got := equity.balance(equity.login("kiprono", "kiprono-phone")); got != 0 {
		t.Fatalf("Kiprono's balance = %.0f after refused credits, want 0", got)
	}
	if status, _ := root.call("GET", "/demo-banks/mail", "", nil); status != http.StatusOK {
		t.Errorf("GET /demo-banks/mail: status %d, want 200", status)
	}
}
