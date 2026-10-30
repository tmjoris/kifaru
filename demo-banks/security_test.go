package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Findings these tests pin down are listed in TESTING.md, together with the
// demo shortcuts that are accepted because every service listens only on
// 127.0.0.1 and everything is synthetic.

func TestSecurity_CustomerRoutesNeedASession(t *testing.T) {
	isolate(t)
	newSwitchStub(t)
	_, server := startBank(t, "ncba")
	a := api{t, server.URL}
	routes := []struct{ method, path string }{
		{"GET", "/api/me"}, {"POST", "/api/transfers"}, {"POST", "/api/withdraw"},
	}
	tokens := map[string]string{"no token": "", "garbage": "garbage", "a guess": strings.Repeat("a", 32), "SQL": "' OR '1'='1", "empty bearer": " "}
	for _, route := range routes {
		for name, token := range tokens {
			t.Run(route.method+" "+route.path+" with "+name, func(t *testing.T) {
				status, reply := a.call(route.method, route.path, token, map[string]any{"bank": "equity", "account": "0114417", "amount": 1})
				if status != http.StatusUnauthorized {
					t.Fatalf("status = %d, want 401 (%v)", status, reply)
				}
			})
		}
	}
}

func TestSecurity_TokensAreRandomAndUnique(t *testing.T) {
	isolate(t)
	_, server := startBank(t, "ncba")
	a := api{t, server.URL}
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		token := a.login("grace", "grace-phone")
		if len(token) != 32 || strings.Trim(token, "0123456789abcdef") != "" {
			t.Fatalf("token %q is not 128 bits of hex", token)
		}
		if seen[token] {
			t.Fatalf("token %q was issued twice", token)
		}
		seen[token] = true
	}
}

func TestSecurity_CustomersCannotReachEachOthersAccounts(t *testing.T) {
	isolate(t)
	newSwitchStub(t)
	_, server := startBank(t, "ncba")
	a := api{t, server.URL}
	grace, peter := a.login("grace", "grace-phone"), a.login("peter", "peter-phone")
	a.pay(grace, "equity", "0114417", "J. Kiprono", 1000)

	_, me := a.call("GET", "/api/me", peter, nil)
	if me["name"] != "Peter Mwangi" || me["balance"] != 268000.0 {
		t.Fatalf("Peter's session shows %v, want Peter's own account", me)
	}
	if transfers, _ := me["transfers"].([]any); len(transfers) != 0 {
		t.Errorf("Peter can see %d of Grace's transfers", len(transfers))
	}
	// A payment always comes out of the session owner's own account.
	a.pay(peter, "equity", "0114417", "J. Kiprono", 2000)
	if got := a.balance(grace); got != 411500 {
		t.Errorf("Grace's balance = %.0f, want only her own KES 1,000 payment taken", got)
	}
}

func TestSecurity_TokensDieOnLogoutAndDoNotCrossBanks(t *testing.T) {
	isolate(t)
	_, ncba := startBank(t, "ncba")
	_, kcb := startBank(t, "kcb")
	a, k := api{t, ncba.URL}, api{t, kcb.URL}
	token := a.login("grace", "grace-phone")
	if status, _ := k.call("GET", "/api/me", token, nil); status != http.StatusUnauthorized {
		t.Errorf("an NCBA token at KCB: status %d, want 401", status)
	}
	a.call("POST", "/api/logout", token, nil)
	if status, _ := a.call("GET", "/api/me", token, nil); status != http.StatusUnauthorized {
		t.Errorf("after signing out: status %d, want 401", status)
	}
}

func TestSecurity_LoginDoesNotRevealWhichCustomersExist(t *testing.T) {
	isolate(t)
	_, server := startBank(t, "ncba")
	a := api{t, server.URL}
	_, wrongPassword := a.call("POST", "/api/login", "", map[string]string{"username": "grace", "password": "wrong", "device": "d"})
	_, unknownUser := a.call("POST", "/api/login", "", map[string]string{"username": "nobody", "password": "wrong", "device": "d"})
	if wrongPassword["error"] != unknownUser["error"] {
		t.Errorf("replies differ: %q and %q", wrongPassword["error"], unknownUser["error"])
	}
	for _, attack := range []string{"' OR '1'='1", "grace'--", `" OR ""="`, "../../etc/passwd", "grace\x00", strings.Repeat("A", 100000)} {
		status, reply := a.call("POST", "/api/login", "", map[string]string{"username": attack, "password": attack, "device": attack})
		if status != http.StatusUnauthorized {
			t.Errorf("username %.20q: status %d %v, want 401", attack, status, reply)
		}
	}
}

func TestSecurity_NoPasswordsInAnyReply(t *testing.T) {
	isolate(t)
	newSwitchStub(t)
	_, server := startBank(t, "ncba")
	a := api{t, server.URL}
	token := a.login("grace", "grace-phone")
	a.pay(token, "equity", "0114417", "J. Kiprono", 1000)
	for _, path := range []string{"/api/me", "/api/ops/state"} {
		_, body, _ := a.raw("GET", path, token, nil)
		if strings.Contains(string(body), customerPassword) || strings.Contains(strings.ToLower(string(body)), `"password"`) {
			t.Errorf("%s leaks a customer password:\n%.400s", path, body)
		}
	}
}

func TestSecurity_AwkwardRequestsNeverCrashOrServerError(t *testing.T) {
	isolate(t)
	newSwitchStub(t)
	_, server := startBank(t, "ncba")
	a := api{t, server.URL}
	token := a.login("grace", "grace-phone")
	bodies := []any{
		"", "null", "[]", "[1,2,3]", `"text"`, "12", "{", `{"amount":"lots"}`, `{"amount":1e999}`,
		`{"bank":null,"account":null,"amount":null}`, `{"bank":["equity"],"account":{"a":1},"amount":[1]}`,
		strings.Repeat("[", 20000), strings.Repeat("a", 2<<20),
		`{"bank":"equity","account":"` + strings.Repeat("9", 100000) + `","name":"x","amount":1}`,
	}
	for i, body := range bodies {
		for _, path := range []string{"/api/transfers", "/api/withdraw", "/api/login", "/admin/reset", "/interbank/credit", "/interbank/return", "/api/ops/hold", "/api/ops/return", "/api/ops/connector"} {
			status, _, _ := a.raw("POST", path, token, body)
			if status >= 500 {
				t.Errorf("body #%d to %s: status %d, want a 4xx answer or success", i, path, status)
			}
		}
	}
	if got := a.balance(token); got < 0 || got > 412500 {
		t.Errorf("balance = %.0f after hostile requests, want it unchanged or lower", got)
	}
}

func TestSecurity_BodyOverOneMegabyteIsRefused(t *testing.T) {
	isolate(t)
	_, server := startBank(t, "ncba")
	a := api{t, server.URL}
	big := `{"username":"grace","password":"` + strings.Repeat("x", 2<<20) + `"}`
	if status, _, _ := a.raw("POST", "/api/login", "", big); status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", status)
	}
}

func TestSecurity_AmountsCannotCreateOrDestroyMoney(t *testing.T) {
	isolate(t)
	newSwitchStub(t)
	_, server := startBank(t, "equity")
	a := api{t, server.URL}
	for _, amount := range []float64{-100000, 0} {
		for _, path := range []string{"/interbank/credit", "/interbank/return"} {
			status, reply := a.call("POST", path, "", map[string]any{"ref": fmt.Sprint("R", amount), "to_account": "0114417",
				"amount": amount, "from_bank": "ncba", "from_bank_name": "NCBA", "returned_by": "ncba", "returned_by_name": "NCBA"})
			if status != http.StatusBadRequest && status != http.StatusNotFound {
				t.Errorf("%s with amount %.0f: status %d %v, want it refused", path, amount, status, reply)
			}
		}
	}
	token := a.login("kiprono", "kiprono-phone")
	if got := a.balance(token); got != 0 {
		t.Errorf("balance = %.0f after non-positive credits and returns, want 0", got)
	}
}

func TestSecurity_MarkupInUserInputStaysData(t *testing.T) {
	isolate(t)
	newSwitchStub(t)
	_, server := startBank(t, "ncba")
	a := api{t, server.URL}
	token := a.login("grace", "grace-phone")
	script := `<script>alert(1)</script><img src=x onerror=alert(2)>`
	a.pay(token, "equity", script, script, 1000)
	for _, path := range []string{"/api/me", "/api/ops/state"} {
		_, body, header := a.raw("GET", path, token, nil)
		if !strings.HasPrefix(header.Get("Content-Type"), "application/json") {
			t.Errorf("%s: Content-Type = %q, want application/json so a browser never renders it", path, header.Get("Content-Type"))
		}
		if strings.Contains(string(body), "<script>") || strings.Contains(string(body), "<img") {
			t.Errorf("%s returns raw markup:\n%.300s", path, body)
		}
	}
}

func TestSecurity_StaticFilesStayInsideTheWebFolder(t *testing.T) {
	isolate(t)
	_, server := startBank(t, "ncba")
	a := api{t, server.URL}
	for _, path := range []string{
		"/static/../bank.go", "/static/%2e%2e/bank.go", "/static/..%2fbank.go", "/static/%2e%2e%2fconfig.go",
		"/static/..\\bank.go", "/static/....//bank.go", "/static/%252e%252e/bank.go", "/static/./../go.mod",
		"/static/web/../../bank.go",
	} {
		status, body, _ := a.raw("GET", path, "", nil)
		if status == http.StatusOK && (strings.Contains(string(body), "package main") || strings.Contains(string(body), "module ")) {
			t.Errorf("GET %s served source code (status %d)", path, status)
		}
	}
	if status, _, _ := a.raw("GET", "/static/style.css", "", nil); status != http.StatusOK {
		t.Errorf("the real stylesheet: status %d, want 200", status)
	}
}

// The ops, reset and interbank routes have no sign-in. That is acceptable for
// a synthetic demo only because nothing listens beyond the loopback interface.
func TestSecurity_ServicesListenOnLoopbackOnly(t *testing.T) {
	for _, port := range []string{"9100", "9101", "9102", "9103"} {
		if got, want := listenAddr(port), "127.0.0.1:"+port; got != want {
			t.Errorf("listenAddr(%s) = %q, want %q", port, got, want)
		}
	}
}
