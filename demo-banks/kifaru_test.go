package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// kifaruStub is a small fake of the Kifaru API: just the routes a bank's
// connector uses. It records every request so tests can inspect what a bank
// actually sends.
type kifaruStub struct {
	mu       sync.Mutex
	paths    map[string]int
	bodies   map[string][]string
	check    RiskCheck
	checkErr int // status to answer risk checks with; 0 means a normal answer
	expire   int // number of sessions to expire (answer 401) before behaving
}

func newKifaruStub(t *testing.T) *kifaruStub {
	t.Helper()
	stub := &kifaruStub{paths: map[string]int{}, bodies: map[string][]string{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		stub.mu.Lock()
		defer stub.mu.Unlock()
		stub.paths[r.URL.Path]++
		stub.bodies[r.URL.Path] = append(stub.bodies[r.URL.Path], string(raw))
		switch r.URL.Path {
		case "/v1/auth/login":
			writeJSON(w, http.StatusOK, map[string]string{"access_token": "token", "csrf_token": "csrf"})
		case "/v1/risk-check":
			if stub.expire > 0 {
				stub.expire--
				writeError(w, http.StatusUnauthorized, "expired")
				return
			}
			if stub.checkErr != 0 {
				writeError(w, stub.checkErr, "risk check failed")
				return
			}
			writeJSON(w, http.StatusOK, stub.check)
		case "/v1/reports":
			writeJSON(w, http.StatusOK, map[string]any{"report_id": "R-1",
				"validation": map[string]any{"status": "CORROBORATED_SIGNAL", "validation_score": 0.9}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("KIFARU_API_URL", server.URL)
	return stub
}

func (s *kifaruStub) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paths[path]
}

func (s *kifaruStub) requests(path string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.bodies[path]...)
}

func connectedBank(t *testing.T, id string) (*Bank, api, *switchStub, *kifaruStub) {
	t.Helper()
	isolate(t)
	sw := newSwitchStub(t)
	kifaru := newKifaruStub(t)
	bank, server := startBank(t, id)
	a := api{t, server.URL}
	if status, _ := a.call("POST", "/api/ops/connector", "", map[string]bool{"enabled": true}); status != http.StatusOK {
		t.Fatalf("connector toggle: status %d", status)
	}
	return bank, a, sw, kifaru
}

func lastScore(t *testing.T, a api) float64 {
	t.Helper()
	items := a.transfers()
	if len(items) == 0 {
		t.Fatal("the bank has no transfers")
	}
	return items[len(items)-1]["score"].(float64)
}

func TestRegression_ConnectorOffSendsNothingToKifaru(t *testing.T) {
	isolate(t)
	newSwitchStub(t)
	kifaru := newKifaruStub(t)
	_, server := startBank(t, "ncba")
	a := api{t, server.URL}
	token := a.login("grace", "attacker-android")
	a.pay(token, "equity", "0114417", "J. Kiprono", 85000)
	time.Sleep(200 * time.Millisecond)
	for _, path := range []string{"/v1/auth/login", "/v1/risk-check", "/v1/reports"} {
		if n := kifaru.count(path); n != 0 {
			t.Errorf("%s called %d times with the connector off", path, n)
		}
	}
}

func TestRegression_FlaggedKifaruAnswersRaiseTheScore(t *testing.T) {
	for _, tc := range []struct {
		name  string
		check RiskCheck
		want  float64
	}{
		{"nothing flagged", RiskCheck{}, 0.60},
		{"device flagged", RiskCheck{Device: RiskSignal{Checked: true, Flagged: true, Tier: "corroborated"}}, 0.95},
		{"beneficiary corroborated", RiskCheck{Destination: RiskSignal{Checked: true, Flagged: true, Tier: "corroborated"}}, 0.99},
		{"beneficiary reported only", RiskCheck{Destination: RiskSignal{Checked: true, Flagged: true, Tier: "reported"}}, 0.80},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, a, _, kifaru := connectedBank(t, "ncba")
			kifaru.check = tc.check
			token := a.login("grace", "attacker-android")
			a.pay(token, "equity", "0114417", "J. Kiprono", 85000)
			if got := lastScore(t, a); !approx(got, tc.want) {
				t.Fatalf("score = %.2f, want %.2f", got, tc.want)
			}
			if tc.want >= 0.70 {
				// A blocked or high-scoring payment is reported; wait so it cannot leak into the next test.
				waitFor(t, 3*time.Second, "the report to reach Kifaru", func() bool { return kifaru.count("/v1/reports") >= 1 })
			}
		})
	}
}

// If Kifaru is down, or says no, the bank scores on its own and carries on.
func TestRegression_KifaruFailureFailsOpen(t *testing.T) {
	t.Run("error answer", func(t *testing.T) {
		bank, a, sw, kifaru := connectedBank(t, "ncba")
		kifaru.checkErr = http.StatusInternalServerError
		token := a.login("grace", "attacker-android")
		_, reply := a.pay(token, "equity", "0114417", "J. Kiprono", 85000)
		if reply["status"] != "completed" || sw.count("transfer") != 1 {
			t.Fatalf("payment should still go through, got %v", reply)
		}
		if got := lastScore(t, a); !approx(got, 0.60) {
			t.Fatalf("score = %.2f, want 0.60 without Kifaru", got)
		}
		if !bank.hasEvent("risk check unavailable") {
			t.Error("the bank's feed should warn that the risk check was unavailable")
		}
	})
	t.Run("unreachable", func(t *testing.T) {
		_, a, _, _ := connectedBank(t, "ncba")
		t.Setenv("KIFARU_API_URL", unreachable)
		token := a.login("grace", "attacker-android")
		if _, reply := a.pay(token, "equity", "0114417", "J. Kiprono", 85000); reply["status"] != "completed" {
			t.Fatalf("payment should still go through, got %v", reply)
		}
	})
}

func TestRegression_ConnectorRenewsAnExpiredKifaruSession(t *testing.T) {
	_, a, _, kifaru := connectedBank(t, "ncba")
	kifaru.expire = 1
	token := a.login("grace", "attacker-android")
	a.pay(token, "equity", "0114417", "J. Kiprono", 85000)
	waitFor(t, 3*time.Second, "the connector to sign in again", func() bool { return kifaru.count("/v1/auth/login") >= 2 })
	if got := lastScore(t, a); !approx(got, 0.60) {
		t.Fatalf("score = %.2f after renewing the session, want 0.60", got)
	}
}

var protectedValue = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Only keyed hashes leave the bank: no account number, name or device ID.
func TestSecurity_ReportsCarryOnlyProtectedIdentifiers(t *testing.T) {
	_, a, _, kifaru := connectedBank(t, "kcb")
	token := a.login("faith", "attacker-iphone")
	a.pay(token, "equity", "0114417", "J. Kiprono", 150000)
	waitFor(t, 3*time.Second, "the report to reach Kifaru", func() bool { return kifaru.count("/v1/reports") == 1 })

	var report map[string]any
	if err := json.Unmarshal([]byte(kifaru.requests("/v1/reports")[0]), &report); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"subject_account_hash", "subject_customer_hash", "destination_account_hash"} {
		if value, _ := report[field].(string); !protectedValue.MatchString(value) {
			t.Errorf("%s = %q, want a sha256 hash", field, value)
		}
	}
	evidence := report["evidence"].(map[string]any)
	if value, _ := evidence["device_profile"].(string); !protectedValue.MatchString(value) {
		t.Errorf("device_profile = %q, want a sha256 hash", value)
	}
	for _, body := range append(kifaru.requests("/v1/reports"), kifaru.requests("/v1/risk-check")...) {
		for _, secret := range []string{"1105108", "0114417", "Faith", "Njeri", "J. Kiprono", "attacker-iphone", "faith"} {
			if strings.Contains(body, secret) {
				t.Errorf("a request to Kifaru contains %q:\n%s", secret, body)
			}
		}
	}
}

func TestSecurity_RiskCheckSendsOnlyProtectedIdentifiers(t *testing.T) {
	_, a, _, kifaru := connectedBank(t, "ncba")
	token := a.login("grace", "attacker-android")
	a.pay(token, "equity", "0114417", "J. Kiprono", 85000)
	bodies := kifaru.requests("/v1/risk-check")
	if len(bodies) == 0 {
		t.Fatal("the bank did not ask Kifaru for a pre-payment check")
	}
	var request map[string]string
	if err := json.Unmarshal([]byte(bodies[len(bodies)-1]), &request); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"device_profile", "destination_account_hash"} {
		if !protectedValue.MatchString(request[field]) {
			t.Errorf("%s = %q, want a sha256 hash", field, request[field])
		}
	}
	if request["destination_institution"] != "psp_c" {
		t.Errorf("destination_institution = %q, want Equity's Kifaru code", request["destination_institution"])
	}
}

func TestRegression_SignInChecksTheDeviceWithKifaru(t *testing.T) {
	bank, a, _, kifaru := connectedBank(t, "kcb")
	kifaru.check = RiskCheck{Device: RiskSignal{Checked: true, Flagged: true, Tier: "corroborated", Institutions: 2}}
	a.login("faith", "attacker-iphone")
	waitFor(t, 3*time.Second, "the sign-in warning", func() bool { return bank.hasEvent("device in 2 other institution") })
	// The warning never blocks the sign-in.
	if _, me := a.call("GET", "/api/me", a.login("faith", "attacker-iphone"), nil); me["name"] != "Faith Njeri" {
		t.Fatalf("sign-in should still work, got %v", me)
	}
}

func TestRegression_ProtectFormat(t *testing.T) {
	if got := protect("salt", "0114417"); !protectedValue.MatchString(got) {
		t.Fatalf("protect() = %q, want sha256:<64 hex>", got)
	}
	if protect("salt", " 011 4417 ") != protect("salt", "0114417") {
		t.Error("spaces must not change the hash")
	}
	if protect("salt", "Browser-1A2B") != protect("salt", "browser-1a2b") {
		t.Error("case must not change the hash")
	}
	if protect("one", "0114417") == protect("two", "0114417") {
		t.Error("a different run key must give a different hash")
	}
	if protect("one", "0114417") == protect("one", "0114418") {
		t.Error("different values must give different hashes")
	}
}

// These digests are the same reference values asserted in src/domain.test.ts,
// so the Go banks and the Kifaru web app are checked against each other.
func TestRegression_ProtectMatchesTheFrontendReferenceDigests(t *testing.T) {
	t.Setenv("DEMO_HASH_KEY", "")
	for _, tc := range []struct{ salt, value, want string }{
		{"abcd", " 011 4417 ", "sha256:ff3c0425cf5dfb97a95242bcb49038a2ad9641f1175e49ff645308c52f1c6f46"},
		{"", "Browser-1A2B", "sha256:59ec62a3bb715c158fca08ca20fe1ef9f8056b22bd8a5afa5ccec9acc000bb9e"},
	} {
		if got := protect(tc.salt, tc.value); got != tc.want {
			t.Errorf("protect(%q, %q) = %s, want %s", tc.salt, tc.value, got, tc.want)
		}
	}
}
