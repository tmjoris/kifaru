package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func creditMessage(ref string, amount float64) map[string]any {
	return map[string]any{"ref": ref, "from_bank": "ncba", "from_bank_name": "NCBA", "from_name": "Grace Achieng",
		"to_bank": "equity", "to_account": "0114417", "amount": amount}
}

func TestRegression_SwitchRejectsBadMessages(t *testing.T) {
	w := newWorld(t)
	for _, tc := range []struct {
		name, path string
		body       any
	}{
		{"transfer to an unknown bank", "/switch/transfer", map[string]any{"ref": "R", "to_bank": "nowhere"}},
		{"transfer without a reference", "/switch/transfer", map[string]any{"to_bank": "equity", "to_account": "0114417", "amount": 5}},
		{"transfer with a blank reference", "/switch/transfer", map[string]any{"ref": "   ", "to_bank": "equity"}},
		{"transfer that is not JSON", "/switch/transfer", "{oops"},
		{"return to an unknown bank", "/switch/return", map[string]any{"ref": "R", "to_bank": "nowhere"}},
		{"return without a reference", "/switch/return", map[string]any{"to_bank": "ncba", "amount": 5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if status, _ := w.net.call("POST", tc.path, "", tc.body); status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", status)
			}
		})
	}
}

func TestRegression_SwitchCreditsOnceAndLogsRepeats(t *testing.T) {
	w := newWorld(t)
	w.reset("without")
	for i := 0; i < 3; i++ {
		status, reply := w.net.call("POST", "/switch/transfer", "", creditMessage("NCBA-1", 85000))
		if status != http.StatusOK {
			t.Fatalf("attempt %d: status %d %v", i+1, status, reply)
		}
		if wantDuplicate := i > 0; (reply["duplicate"] == true) != wantDuplicate {
			t.Errorf("attempt %d: duplicate = %v, want %v", i+1, reply["duplicate"], wantDuplicate)
		}
	}
	if got := w.balance("equity", "kiprono", "checker"); got != 85000 {
		t.Errorf("balance = %.0f after the same credit three times, want 85000", got)
	}
	_, state := w.net.call("GET", "/api/state", "", nil)
	log, _ := state["switch"].([]any)
	repeats := 0
	for _, entry := range log {
		if strings.Contains(entry.(map[string]any)["status"].(string), "repeat ignored") {
			repeats++
		}
	}
	if len(log) != 3 || repeats != 2 {
		t.Errorf("switch log has %d entries with %d repeats, want 3 and 2", len(log), repeats)
	}
}

func TestRegression_SwitchReturnsOnce(t *testing.T) {
	w := newWorld(t)
	w.reset("without")
	token := w.bank["ncba"].login("grace", "grace-phone")
	_, sent := w.bank["ncba"].pay(token, "equity", "0114417", "J. Kiprono", 85000)
	back := map[string]any{"ref": sent["ref"], "to_bank": "ncba", "amount": 85000, "returned_by": "equity", "returned_by_name": "Equity Bank"}
	for i := 0; i < 2; i++ {
		if status, _ := w.net.call("POST", "/switch/return", "", back); status != http.StatusOK {
			t.Fatalf("return %d: status %d", i+1, status)
		}
	}
	if got := w.bank["ncba"].balance(token); got != 412500 {
		t.Errorf("Grace has %.0f after the same return twice, want 412500", got)
	}
}

// A credit the receiving bank could not apply is not recorded as settled, so a
// retry of the same message is delivered.
func TestRegression_FailedDeliveryCanBeRetried(t *testing.T) {
	w := newWorld(t)
	var calls atomic.Int32
	flaky := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			writeError(rw, http.StatusInternalServerError, "temporarily down")
			return
		}
		writeJSON(rw, http.StatusOK, map[string]bool{"ok": true})
	}))
	defer flaky.Close()
	t.Setenv("EQUITY_URL", flaky.URL)
	status, _ := w.net.call("POST", "/switch/transfer", "", creditMessage("NCBA-9", 5000))
	if status != http.StatusBadGateway {
		t.Fatalf("first attempt: status %d, want 502", status)
	}
	status, reply := w.net.call("POST", "/switch/transfer", "", creditMessage("NCBA-9", 5000))
	if status != http.StatusOK || reply["duplicate"] == true {
		t.Fatalf("retry: %d %v, want a fresh delivery", status, reply)
	}
	if calls.Load() != 2 {
		t.Errorf("the receiving bank was called %d times, want 2", calls.Load())
	}
}

// Many copies of one message arriving together credit the account once.
func TestRegression_ConcurrentIdenticalCreditsApplyOnce(t *testing.T) {
	w := newWorld(t)
	w.reset("without")
	var wg sync.WaitGroup
	statuses := make(chan int, 30)
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, _, _ := w.net.raw("POST", "/switch/transfer", "", creditMessage("NCBA-RACE", 10000))
			statuses <- status
		}()
	}
	wg.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusOK && status != http.StatusConflict {
			t.Errorf("status %d, want 200 or 409", status)
		}
	}
	if got := w.balance("equity", "kiprono", "checker"); got != 10000 {
		t.Fatalf("balance = %.0f after 30 identical credits, want 10000", got)
	}
}

func TestRegression_MailboxFiltersByBank(t *testing.T) {
	w := newWorld(t)
	w.net.call("POST", "/mail", "", map[string]string{"from_bank": "ncba", "to_bank": "equity", "subject": "Suspected mule account", "body": "b"})
	w.net.call("POST", "/mail", "", map[string]string{"from_bank": "ncba", "to_bank": "kcb", "subject": "Other", "body": "b"})
	count := func(query string) int {
		_, reply := w.net.call("GET", "/mail"+query, "", nil)
		return len(reply["mail"].([]any))
	}
	if count("") != 2 || count("?bank=equity") != 1 || count("?bank=ncba") != 0 {
		t.Errorf("mailbox counts: all %d, equity %d, ncba %d; want 2, 1, 0", count(""), count("?bank=equity"), count("?bank=ncba"))
	}
}

func TestRegression_DirectorModes(t *testing.T) {
	w := newWorld(t)
	for _, bad := range []any{map[string]string{"mode": "maybe"}, map[string]string{}, "{nope"} {
		if status, _ := w.net.call("POST", "/api/mode", "", bad); status != http.StatusBadRequest {
			t.Errorf("mode %v: status %d, want 400", bad, status)
		}
	}
	w.reset("with")
	keys := map[string]bool{}
	for id := range w.bank {
		_, state := w.bank[id].call("GET", "/api/ops/state", "", nil)
		if state["connector"] != true {
			t.Errorf("%s connector = %v, want on in the With Kifaru mode", id, state["connector"])
		}
		keys[state["run_key"].(string)] = true
	}
	if len(keys) != 1 {
		t.Errorf("banks have %d different run keys, want one shared key", len(keys))
	}
	first := keys
	w.reset("without")
	for id := range w.bank {
		_, state := w.bank[id].call("GET", "/api/ops/state", "", nil)
		if state["connector"] != false {
			t.Errorf("%s connector = %v, want off in the Without Kifaru mode", id, state["connector"])
		}
		if first[state["run_key"].(string)] {
			t.Errorf("%s reused the earlier run key", id)
		}
	}
	_, state := w.net.call("GET", "/api/state", "", nil)
	if state["mode"] != "without" || state["step"] != 0.0 || len(state["steps"].([]any)) != 6 {
		t.Errorf("director state after reset = mode %v, step %v, %d steps", state["mode"], state["step"], len(state["steps"].([]any)))
	}
}

func TestRegression_DirectorSteps(t *testing.T) {
	for mode, wantStep4 := range map[string]string{
		"without": "Switch to a new phone and take over Faith's KCB account, KES 150,000",
		"with":    "Switch to a new phone and try Faith's KCB account, KES 150,000; KCB checks Kifaru first",
	} {
		steps := autopilotSteps(mode)
		if len(steps) != 6 || steps[3].Title != wantStep4 {
			t.Errorf("%s: %d steps, step 4 = %q", mode, len(steps), steps[3].Title)
		}
	}
}

func TestRegression_DirectorStopsAtTheLastStep(t *testing.T) {
	w := newWorld(t)
	w.reset("without")
	w.network.mu.Lock()
	w.network.step = len(autopilotSteps("without"))
	w.network.mu.Unlock()
	if status, _ := w.net.call("POST", "/api/autopilot/next", "", map[string]any{}); status != http.StatusConflict {
		t.Fatalf("status %d, want 409 once the attack is complete", status)
	}
}

// This is the scripted attack from TestWork.md with the connectors off: the
// money is gone and nobody could stop it. Each step checks the ledgers.
func TestRegression_DirectorReplayWithoutKifaru(t *testing.T) {
	w := newWorld(t)
	w.reset("without")
	next := func(step int) map[string]any {
		t.Helper()
		status, reply := w.net.call("POST", "/api/autopilot/next", "", map[string]any{})
		if status != http.StatusOK {
			t.Fatalf("step %d: status %d %v", step, status, reply)
		}
		return reply
	}
	balances := func() (grace, peter, faith, kiprono float64) {
		return w.balance("ncba", "grace", "checker"), w.balance("ncba", "peter", "checker"),
			w.balance("kcb", "faith", "checker"), w.balance("equity", "kiprono", "checker")
	}
	type want struct{ grace, peter, faith, kiprono float64 }
	steps := []struct {
		name string
		want want
	}{
		{"Grace sends KES 85,000 and it completes", want{327500, 268000, 530000, 85000}},
		{"Peter's payment from the same phone is blocked", want{327500, 268000, 530000, 85000}},
		{"Equity has nothing to act on", want{327500, 268000, 530000, 85000}},
		{"Faith's KES 150,000 goes through at KCB", want{327500, 268000, 380000, 235000}},
		{"the mule account is cashed out", want{327500, 268000, 380000, 0}},
		{"there is nothing left to return", want{327500, 268000, 380000, 0}},
	}
	for i, step := range steps {
		state := next(i + 1)
		if state["step"] != float64(i+1) {
			t.Fatalf("after %q the director is at step %v, want %d", step.name, state["step"], i+1)
		}
		grace, peter, faith, kiprono := balances()
		got := want{grace, peter, faith, kiprono}
		if got != step.want {
			t.Fatalf("after step %d (%s): balances %+v, want %+v", i+1, step.name, got, step.want)
		}
		switch i {
		case 0:
			if s := w.bank["ncba"].transfers(); !approx(s[len(s)-1]["score"].(float64), 0.60) || s[len(s)-1]["status"] != "completed" {
				t.Errorf("step 1: %v, want completed at 0.60", s[len(s)-1])
			}
		case 1:
			s := w.bank["ncba"].transfers()
			if !approx(s[len(s)-1]["score"].(float64), 0.99) || s[len(s)-1]["status"] != "blocked" {
				t.Errorf("step 2: %v, want blocked at 0.99", s[len(s)-1])
			}
		case 3:
			s := w.bank["kcb"].transfers()
			if !approx(s[len(s)-1]["score"].(float64), 0.60) || s[len(s)-1]["status"] != "completed" {
				t.Errorf("step 4: %v, want completed at 0.60", s[len(s)-1])
			}
		}
	}

	// NCBA could only email Equity's mailbox, and nobody read it. Without Kifaru
	// Equity never received an alert.
	waitFor(t, 8*time.Second, "NCBA's email to reach the shared mailbox", func() bool {
		_, reply := w.net.call("GET", "/mail?bank=equity", "", nil)
		return len(reply["mail"].([]any)) >= 1
	})
	_, equity := w.bank["equity"].call("GET", "/api/ops/state", "", nil)
	if alerts := equity["alerts"].([]any); len(alerts) != 0 {
		t.Errorf("Equity has %d Kifaru alerts without Kifaru, want none", len(alerts))
	}
	for id, bank := range w.banks {
		if id != "equity" && bank.hasEvent("Kifaru connector error") {
			t.Errorf("%s tried to reach Kifaru with the connector off", id)
		}
	}
	awaitFraudDesk(t, w.banks["ncba"])
	if status, _ := w.net.call("POST", "/api/autopilot/next", "", map[string]any{}); status != http.StatusConflict {
		t.Errorf("a seventh step: status %d, want 409", status)
	}
}
