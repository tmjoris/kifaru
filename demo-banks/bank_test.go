package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRegression_SignIn(t *testing.T) {
	isolate(t)
	_, server := startBank(t, "ncba")
	a := api{t, server.URL}
	for _, tc := range []struct {
		name, user, pass string
		want             int
	}{
		{"valid", "grace", customerPassword, http.StatusOK},
		{"user name ignores case and spaces", "  GRACE ", customerPassword, http.StatusOK},
		{"wrong password", "grace", "nope", http.StatusUnauthorized},
		{"unknown customer", "mallory", customerPassword, http.StatusUnauthorized},
		{"a customer of another bank", "faith", customerPassword, http.StatusUnauthorized},
		{"empty", "", "", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, reply := a.call("POST", "/api/login", "", map[string]string{"username": tc.user, "password": tc.pass, "device": "d"})
			if status != tc.want {
				t.Fatalf("status = %d, want %d (%v)", status, tc.want, reply)
			}
		})
	}
	if status, _ := a.call("POST", "/api/login", "", "{not json"); status != http.StatusBadRequest {
		t.Errorf("malformed body: status %d, want 400", status)
	}
}

func TestRegression_FirstPaymentFromANewDeviceGoesThrough(t *testing.T) {
	isolate(t)
	sw := newSwitchStub(t)
	_, server := startBank(t, "ncba")
	a := api{t, server.URL}
	token := a.login("grace", "attacker-android")

	status, reply := a.pay(token, "equity", "0114417", "J. Kiprono", 85000)
	if status != http.StatusOK || reply["status"] != "completed" {
		t.Fatalf("status %d, reply %v", status, reply)
	}
	ref, _ := reply["ref"].(string)
	if !strings.HasPrefix(ref, "NCBA-") {
		t.Errorf("ref = %q, want an NCBA reference", ref)
	}
	if got := a.balance(token); got != 327500 {
		t.Errorf("balance = %.0f, want 327500", got)
	}
	if sw.count("transfer") != 1 {
		t.Fatalf("the switch received %d transfers, want 1", sw.count("transfer"))
	}
	sent := sw.transfers[0]
	if sent["ref"] != ref || sent["to_bank"] != "equity" || sent["to_account"] != "0114417" || sent["amount"] != 85000.0 {
		t.Errorf("switch message = %v", sent)
	}
	if got := lastScoreOf(a); !approx(got, 0.60) {
		t.Errorf("score = %.2f, want 0.60", got)
	}
}

func lastScoreOf(a api) float64 {
	items := a.transfers()
	return items[len(items)-1]["score"].(float64)
}

func TestRegression_RepeatPaymentIsBlockedFromANewDeviceButNotAKnownOne(t *testing.T) {
	for _, tc := range []struct {
		device string
		status string
		score  float64
	}{{"attacker-android", "blocked", 0.85}, {"grace-phone", "completed", 0.60}} {
		t.Run(tc.device, func(t *testing.T) {
			isolate(t)
			newSwitchStub(t)
			bank, server := startBank(t, "ncba")
			a := api{t, server.URL}
			token := a.login("grace", tc.device)
			a.pay(token, "equity", "0114417", "J. Kiprono", 85000)
			_, reply := a.pay(token, "equity", "0114417", "J. Kiprono", 85000)
			if reply["status"] != tc.status {
				t.Fatalf("second payment: %v, want %s", reply["status"], tc.status)
			}
			if got := lastScoreOf(a); !approx(got, tc.score) {
				t.Errorf("score = %.2f, want %.2f", got, tc.score)
			}
			if tc.status == "blocked" {
				awaitFraudDesk(t, bank)
			}
		})
	}
}

func TestRegression_ABlockLocksOutTheDevice(t *testing.T) {
	isolate(t)
	newSwitchStub(t)
	bank, server := startBank(t, "ncba")
	a := api{t, server.URL}
	attacker := a.login("grace", "attacker-android")
	other := a.login("grace", "attacker-android") // a second session on the same device
	owner := a.login("grace", "grace-phone")      // the owner's real phone
	a.pay(attacker, "equity", "0114417", "J. Kiprono", 85000)
	_, reply := a.pay(attacker, "equity", "0114417", "J. Kiprono", 85000)
	if reply["status"] != "blocked" {
		t.Fatalf("second payment: %v, want blocked", reply)
	}
	awaitFraudDesk(t, bank)
	before := a.balance(owner)

	for name, token := range map[string]string{"the paying session": attacker, "another session on that device": other} {
		status, reply := a.call("GET", "/api/me", token, nil)
		if status != http.StatusUnauthorized || !strings.Contains(reply["error"].(string), "contact customer care") {
			t.Errorf("%s: status %d %v, want 401 asking to contact customer care", name, status, reply)
		}
	}
	if status, _ := a.call("GET", "/api/me", owner, nil); status != http.StatusOK {
		t.Errorf("a session on a different device must survive, got %d", status)
	}
	status, _ := a.call("POST", "/api/login", "", map[string]string{"username": "grace", "password": customerPassword, "device": "attacker-android"})
	if status != http.StatusForbidden {
		t.Errorf("sign-in from the blocked device: status %d, want 403", status)
	}
	a.login("grace", "grace-phone") // other devices still sign in
	if a.balance(owner) != before {
		t.Error("the blocked payment must not move money")
	}

	// A reset clears the lockout.
	if status, _ := a.call("POST", "/admin/reset", "", map[string]any{"salt": "next-run", "connector": false}); status != http.StatusOK {
		t.Fatalf("reset: status %d", status)
	}
	a.login("grace", "attacker-android")
}

func TestRegression_FraudDeskConfirmsEarlierPaymentsToTheSameBeneficiary(t *testing.T) {
	isolate(t)
	sw := newSwitchStub(t)
	bank, server := startBank(t, "ncba")
	a := api{t, server.URL}
	grace := a.login("grace", "attacker-android")
	a.pay(grace, "equity", "0114417", "J. Kiprono", 85000)
	peter := a.login("peter", "attacker-android") // the attacker moves on to Peter, same phone
	_, reply := a.pay(peter, "equity", "0114417", "J. Kiprono", 120000)
	if reply["status"] != "blocked" {
		t.Fatalf("Peter's payment: %v, want blocked", reply)
	}
	if got := lastScoreOf(a); !approx(got, 0.99) {
		t.Errorf("Peter's score = %.2f, want 0.99", got)
	}
	waitFor(t, 8*time.Second, "Grace to confirm the fraud", func() bool { return bank.hasEvent("confirms they did not make") })
	awaitFraudDesk(t, bank)
	if sw.count("mail") != 1 {
		t.Fatalf("Equity's mailbox got %d emails, want 1", sw.count("mail"))
	}
	if subject := sw.mail[0]["subject"].(string); !strings.Contains(subject, "0114417") {
		t.Errorf("email subject = %q, want the mule account", subject)
	}
	confirmed := 0
	for _, item := range a.transfers() {
		if item["fraud_confirmed"] == true {
			confirmed++
		}
	}
	if confirmed != 1 {
		t.Errorf("%d transfers confirmed as fraud, want Grace's one", confirmed)
	}
}

func TestRegression_PaymentValidation(t *testing.T) {
	isolate(t)
	sw := newSwitchStub(t)
	_, server := startBank(t, "ncba")
	a := api{t, server.URL}
	token := a.login("grace", "grace-phone")
	for _, tc := range []struct {
		name    string
		bank    string
		account string
		amount  float64
	}{
		{"zero", "equity", "0114417", 0},
		{"negative", "equity", "0114417", -5},
		{"more than the balance", "equity", "0114417", 412501},
		{"no account number", "equity", "  ", 1000},
		{"unknown bank", "nowhere", "0114417", 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, reply := a.pay(token, tc.bank, tc.account, "x", tc.amount)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%v)", status, reply)
			}
		})
	}
	if status, _ := a.call("POST", "/api/transfers", token, "{broken"); status != http.StatusBadRequest {
		t.Errorf("malformed body: status %d, want 400", status)
	}
	if sw.count("transfer") != 0 || a.balance(token) != 412500 {
		t.Error("a refused payment must not reach the switch or change the balance")
	}
	// The whole balance can be sent.
	if _, reply := a.pay(token, "equity", "0114417", "J. Kiprono", 412500); reply["status"] != "completed" && reply["status"] != "blocked" {
		t.Errorf("sending the full balance: %v", reply)
	}
}

func TestRegression_FailedSwitchGivesTheMoneyBack(t *testing.T) {
	isolate(t)
	sw := newSwitchStub(t)
	sw.failing = true
	_, server := startBank(t, "ncba")
	a := api{t, server.URL}
	token := a.login("grace", "grace-phone")
	status, _ := a.pay(token, "equity", "0114417", "J. Kiprono", 20000)
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", status)
	}
	if got := a.balance(token); got != 412500 {
		t.Errorf("balance = %.0f after a failed switch, want 412500", got)
	}
	if items := a.transfers(); items[len(items)-1]["status"] != "failed" {
		t.Errorf("transfer status = %v, want failed", items[len(items)-1]["status"])
	}
}

func TestRegression_HeldAccountCannotSendOrWithdraw(t *testing.T) {
	isolate(t)
	newSwitchStub(t)
	_, server := startBank(t, "equity")
	a := api{t, server.URL}
	credit := map[string]any{"ref": "NCBA-1", "from_bank": "ncba", "from_bank_name": "NCBA", "from_name": "Grace", "to_account": "0114417", "amount": 85000}
	a.call("POST", "/interbank/credit", "", credit)
	token := a.login("kiprono", "attacker-laptop")
	if status, _ := a.call("POST", "/api/ops/hold", "", map[string]string{"account": "0114417"}); status != http.StatusOK {
		t.Fatalf("hold: status %d", status)
	}
	if status, _ := a.pay(token, "ncba", "1002291", "G", 1000); status != http.StatusForbidden {
		t.Errorf("payment from a held account: status %d, want 403", status)
	}
	status, reply := a.call("POST", "/api/withdraw", token, map[string]any{"amount": 85000})
	if status != http.StatusForbidden || reply["status"] != "declined" {
		t.Errorf("withdrawal from a held account: %d %v, want a declined 403", status, reply)
	}
	if got := a.balance(token); got != 85000 {
		t.Errorf("balance = %.0f, want the money to stay at 85000", got)
	}
}

func TestRegression_WithdrawalPaysOutAndValidates(t *testing.T) {
	isolate(t)
	newSwitchStub(t)
	_, server := startBank(t, "equity")
	a := api{t, server.URL}
	a.call("POST", "/interbank/credit", "", map[string]any{"ref": "NCBA-1", "from_bank": "ncba", "from_bank_name": "NCBA", "from_name": "Grace", "to_account": "0114417", "amount": 85000})
	token := a.login("kiprono", "attacker-laptop")
	for _, amount := range []float64{0, -1, 85001} {
		if status, _ := a.call("POST", "/api/withdraw", token, map[string]any{"amount": amount}); status != http.StatusBadRequest {
			t.Errorf("withdrawing %.0f: status %d, want 400", amount, status)
		}
	}
	status, reply := a.call("POST", "/api/withdraw", token, map[string]any{"amount": 85000, "channel": "agent"})
	if status != http.StatusOK || reply["status"] != "paid out" {
		t.Fatalf("withdrawal: %d %v", status, reply)
	}
	if got := a.balance(token); got != 0 {
		t.Errorf("balance = %.0f, want 0", got)
	}
}

func TestRegression_InterbankCredits(t *testing.T) {
	isolate(t)
	newSwitchStub(t)
	_, server := startBank(t, "equity")
	a := api{t, server.URL}
	credit := func(ref string, amount float64) (int, map[string]any) {
		return a.call("POST", "/interbank/credit", "", map[string]any{"ref": ref, "from_bank": "ncba", "from_bank_name": "NCBA",
			"from_name": "Grace", "to_account": "0114417", "amount": amount})
	}
	token := a.login("kiprono", "kiprono-phone")
	if status, _ := credit("NCBA-1", 85000); status != http.StatusOK {
		t.Fatalf("credit: status %d", status)
	}
	if status, reply := credit("NCBA-1", 85000); status != http.StatusOK || reply["duplicate"] != true {
		t.Errorf("repeating a reference: %d %v, want an ok duplicate", status, reply)
	}
	if got := a.balance(token); got != 85000 {
		t.Errorf("balance = %.0f after a repeated credit, want 85000", got)
	}
	if status, _ := a.call("POST", "/interbank/credit", "", map[string]any{"ref": "X", "to_account": "9999999", "amount": 5}); status != http.StatusNotFound {
		t.Errorf("credit to an unknown account: status %d, want 404", status)
	}

	// Money that lands after a hold is frozen on arrival, and a hold freezes what is already there.
	a.call("POST", "/api/ops/hold", "", map[string]string{"account": "0114417"})
	credit("KCB-1", 150000)
	_, state := a.call("GET", "/api/ops/state", "", nil)
	states := map[string]string{}
	for _, item := range state["credits"].([]any) {
		c := item.(map[string]any)
		states[c["ref"].(string)] = c["state"].(string)
	}
	if states["NCBA-1"] != "frozen" || states["KCB-1"] != "frozen" {
		t.Errorf("credit states = %v, want both frozen", states)
	}
}

func TestRegression_ReturnsGiveFundsBackOnce(t *testing.T) {
	isolate(t)
	newSwitchStub(t)
	_, server := startBank(t, "ncba")
	a := api{t, server.URL}
	token := a.login("grace", "grace-phone")
	_, reply := a.pay(token, "equity", "0114417", "J. Kiprono", 85000)
	ref := reply["ref"]
	back := map[string]any{"ref": ref, "amount": 85000, "returned_by": "equity", "returned_by_name": "Equity Bank"}
	if status, _ := a.call("POST", "/interbank/return", "", back); status != http.StatusOK {
		t.Fatalf("return: status %d", status)
	}
	if status, _ := a.call("POST", "/interbank/return", "", back); status != http.StatusNotFound {
		t.Errorf("repeating a return: status %d, want 404", status)
	}
	if got := a.balance(token); got != 412500 {
		t.Errorf("balance = %.0f, want the 85000 refunded exactly once (412500)", got)
	}
	if status, _ := a.call("POST", "/interbank/return", "", map[string]any{"ref": "NCBA-nope", "amount": 5}); status != http.StatusNotFound {
		t.Errorf("returning an unknown reference: status %d, want 404", status)
	}
}

func TestRegression_OpsReturnSendsFrozenMoneyBackThroughTheSwitch(t *testing.T) {
	isolate(t)
	sw := newSwitchStub(t)
	_, server := startBank(t, "equity")
	a := api{t, server.URL}
	a.call("POST", "/interbank/credit", "", map[string]any{"ref": "NCBA-1", "from_bank": "ncba", "from_bank_name": "NCBA", "from_name": "Grace", "to_account": "0114417", "amount": 85000})
	a.call("POST", "/api/ops/hold", "", map[string]string{"account": "0114417"})
	status, reply := a.call("POST", "/api/ops/return", "", map[string]string{"account": "0114417"})
	if status != http.StatusOK || reply["returned"] != 85000.0 {
		t.Fatalf("return: %d %v, want 85000 returned", status, reply)
	}
	if sw.count("return") != 1 || sw.returns[0]["to_bank"] != "ncba" || sw.returns[0]["ref"] != "NCBA-1" {
		t.Errorf("switch returns = %v", sw.returns)
	}
	_, again := a.call("POST", "/api/ops/return", "", map[string]string{"account": "0114417"})
	if again["returned"] != 0.0 {
		t.Errorf("a second return moved %v, want nothing", again["returned"])
	}
	if status, _ := a.call("POST", "/api/ops/return", "", map[string]string{"account": "9999999"}); status != http.StatusNotFound {
		t.Errorf("return on an unknown account: status %d, want 404", status)
	}
}

func TestRegression_FailedReturnIsNotMarkedReturned(t *testing.T) {
	isolate(t)
	sw := newSwitchStub(t)
	_, server := startBank(t, "equity")
	a := api{t, server.URL}
	a.call("POST", "/interbank/credit", "", map[string]any{"ref": "NCBA-1", "from_bank": "ncba", "from_bank_name": "NCBA", "from_name": "Grace", "to_account": "0114417", "amount": 85000})
	sw.failing = true
	_, reply := a.call("POST", "/api/ops/return", "", map[string]string{"account": "0114417"})
	if reply["returned"] != 0.0 {
		t.Fatalf("returned %v while the switch was down", reply["returned"])
	}
	sw.failing = false
	_, reply = a.call("POST", "/api/ops/return", "", map[string]string{"account": "0114417"})
	if reply["returned"] != 85000.0 {
		t.Errorf("retry returned %v, want 85000", reply["returned"])
	}
}

func TestRegression_SessionsExpireWhenIdle(t *testing.T) {
	isolate(t)
	newSwitchStub(t)
	bank, server := startBank(t, "ncba")
	a := api{t, server.URL}
	token := a.login("grace", "grace-phone")

	age := func(d time.Duration) {
		bank.mu.Lock()
		current := bank.sessions[token]
		current.lastActive = time.Now().Add(-d)
		bank.sessions[token] = current
		bank.mu.Unlock()
	}
	lastActive := func() time.Time {
		bank.mu.Lock()
		defer bank.mu.Unlock()
		return bank.sessions[token].lastActive
	}

	age(14 * time.Minute)
	stamp := lastActive()
	if status, _ := a.call("GET", "/api/me", token, nil); status != http.StatusOK {
		t.Fatalf("14 idle minutes: status %d, want 200", status)
	}
	if !lastActive().Equal(stamp) {
		t.Error("a page refresh (/api/me) must not extend the session")
	}
	if status, _ := a.pay(token, "equity", "0114417", "J. Kiprono", 1000); status != http.StatusOK {
		t.Fatalf("payment: status %d", status)
	}
	if !lastActive().After(stamp) {
		t.Error("a payment must extend the session")
	}

	age(16 * time.Minute)
	status, reply := a.call("GET", "/api/me", token, nil)
	if status != http.StatusUnauthorized || !strings.Contains(reply["error"].(string), "signed out after 15 minutes") {
		t.Fatalf("16 idle minutes: %d %v, want 401 explaining the 15 minute limit", status, reply)
	}
	if !bank.hasEvent("session ended after 15 minutes") {
		t.Error("the operations feed should record the expiry")
	}
	if status, reply := a.call("GET", "/api/me", token, nil); status != http.StatusUnauthorized || !strings.Contains(reply["error"].(string), "signed out after") {
		t.Errorf("the reason must stay available on later requests, got %d %v", status, reply)
	}
}

func TestRegression_IdleLimitIsConfigurable(t *testing.T) {
	isolate(t)
	t.Setenv("SESSION_IDLE_MINUTES", "1")
	bank, server := startBank(t, "ncba")
	a := api{t, server.URL}
	token := a.login("grace", "grace-phone")
	bank.mu.Lock()
	current := bank.sessions[token]
	current.lastActive = time.Now().Add(-2 * time.Minute)
	bank.sessions[token] = current
	bank.mu.Unlock()
	_, reply := a.call("GET", "/api/me", token, nil)
	if !strings.Contains(reply["error"].(string), "after 1 minute without activity") {
		t.Fatalf("error = %v, want the 1 minute limit", reply["error"])
	}
	for _, bad := range []string{"0", "-3", "soon"} {
		t.Setenv("SESSION_IDLE_MINUTES", bad)
		if sessionIdle() != 15*time.Minute {
			t.Errorf("SESSION_IDLE_MINUTES=%q should fall back to 15 minutes", bad)
		}
	}
}

func TestRegression_ResetRestoresEverythingAndSetsTheRunKey(t *testing.T) {
	isolate(t)
	newSwitchStub(t)
	_, server := startBank(t, "ncba")
	a := api{t, server.URL}
	token := a.login("grace", "grace-phone")
	a.pay(token, "equity", "0114417", "J. Kiprono", 85000)
	if status, _ := a.call("POST", "/admin/reset", "", map[string]any{}); status != http.StatusBadRequest {
		t.Errorf("reset without a run key: status %d, want 400", status)
	}
	if status, _ := a.call("POST", "/admin/reset", "", map[string]any{"salt": "run-2", "connector": true}); status != http.StatusOK {
		t.Fatalf("reset: status %d", status)
	}
	if status, _ := a.call("GET", "/api/me", token, nil); status != http.StatusUnauthorized {
		t.Error("a reset must end every session")
	}
	fresh := a.login("grace", "grace-phone")
	if got := a.balance(fresh); got != 412500 {
		t.Errorf("balance = %.0f after reset, want 412500", got)
	}
	_, state := a.call("GET", "/api/ops/state", "", nil)
	if state["run_key"] != "run-2" || state["connector"] != true {
		t.Errorf("run_key = %v, connector = %v, want run-2 and on", state["run_key"], state["connector"])
	}
	if len(a.transfers()) != 0 {
		t.Error("a reset must clear the transfers")
	}
}

func TestRegression_ConnectorSwitch(t *testing.T) {
	isolate(t)
	_, server := startBank(t, "ncba")
	a := api{t, server.URL}
	for _, enabled := range []bool{true, false} {
		_, reply := a.call("POST", "/api/ops/connector", "", map[string]bool{"enabled": enabled})
		if reply["connector"] != enabled {
			t.Errorf("connector = %v, want %v", reply["connector"], enabled)
		}
		_, state := a.call("GET", "/api/ops/state", "", nil)
		if state["connector"] != enabled {
			t.Errorf("ops state connector = %v, want %v", state["connector"], enabled)
		}
	}
}
