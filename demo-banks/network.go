package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

func postJSON(url string, in, out any) error {
	return sendJSON(http.MethodPost, url, "", in, out)
}

func sendJSON(method, url, token string, in, out any) error {
	var reader io.Reader
	if in != nil {
		encoded, _ := json.Marshal(in)
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		return err
	}
	if in != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode >= 300 {
		var problem struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &problem)
		if problem.Error == "" {
			problem.Error = response.Status
		}
		return &httpError{response.StatusCode, problem.Error}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

type httpError struct {
	status  int
	message string
}

func (e *httpError) Error() string { return e.message }

type Mail struct {
	At       time.Time `json:"at"`
	FromBank string    `json:"from_bank"`
	FromName string    `json:"from_name"`
	ToBank   string    `json:"to_bank"`
	Subject  string    `json:"subject"`
	Body     string    `json:"body"`
}

type SwitchEntry struct {
	At     time.Time `json:"at"`
	Text   string    `json:"text"`
	Status string    `json:"status"`
}

type LogLine struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
	Tone string    `json:"tone"`
}

// Network is the shared infrastructure between the banks: the interbank
// switch, an email relay standing in for fraud-team mailboxes, and the
// presenter's director page with the attacker autopilot.
type Network struct {
	mu        sync.Mutex
	mode      string
	salt      string
	step      int
	busy      bool
	mail      []Mail
	switchLog []SwitchEntry
	attacker  []LogLine
	prepared  string
}

func newNetwork() *Network {
	return &Network{
		mode: "without", salt: randomID(4),
		mail: []Mail{}, switchLog: []SwitchEntry{}, attacker: []LogLine{},
	}
}

func (n *Network) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", page("director.html"))
	mux.Handle("GET /static/", staticFiles())
	mux.HandleFunc("POST /switch/transfer", n.switchTransfer)
	mux.HandleFunc("POST /switch/return", n.switchReturn)
	mux.HandleFunc("POST /mail", n.postMail)
	mux.HandleFunc("GET /mail", n.listMail)
	mux.HandleFunc("GET /api/state", n.state)
	mux.HandleFunc("POST /api/mode", n.setMode)
	mux.HandleFunc("POST /api/prepare", n.prepare)
	mux.HandleFunc("POST /api/autopilot/next", n.autopilotNext)
	return mux
}

func (n *Network) switchTransfer(w http.ResponseWriter, r *http.Request) {
	var request map[string]any
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	to := fmt.Sprint(request["to_bank"])
	if _, ok := bankByID(to); !ok {
		writeError(w, http.StatusBadRequest, "unknown destination bank")
		return
	}
	err := postJSON(bankURL(to)+"/interbank/credit", request, nil)
	amount, _ := request["amount"].(float64)
	entry := SwitchEntry{At: time.Now(), Status: "settled", Text: fmt.Sprintf("%s → %s %s · %s",
		request["from_bank_name"], bankName(to), request["to_account"], kes(amount))}
	if err != nil {
		entry.Status = "rejected: " + err.Error()
	}
	n.mu.Lock()
	n.switchLog = append(n.switchLog, entry)
	n.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (n *Network) switchReturn(w http.ResponseWriter, r *http.Request) {
	var request map[string]any
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	to := fmt.Sprint(request["to_bank"])
	if _, ok := bankByID(to); !ok {
		writeError(w, http.StatusBadRequest, "unknown bank")
		return
	}
	err := postJSON(bankURL(to)+"/interbank/return", request, nil)
	amount, _ := request["amount"].(float64)
	entry := SwitchEntry{At: time.Now(), Status: "returned", Text: fmt.Sprintf("%s → %s · %s returned",
		request["returned_by_name"], bankName(to), kes(amount))}
	if err != nil {
		entry.Status = "rejected: " + err.Error()
	}
	n.mu.Lock()
	n.switchLog = append(n.switchLog, entry)
	n.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (n *Network) postMail(w http.ResponseWriter, r *http.Request) {
	var mail Mail
	if err := decode(r, &mail); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	mail.At = time.Now()
	n.mu.Lock()
	n.mail = append(n.mail, mail)
	n.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (n *Network) listMail(w http.ResponseWriter, r *http.Request) {
	bank := r.URL.Query().Get("bank")
	n.mu.Lock()
	defer n.mu.Unlock()
	matched := []Mail{}
	for _, mail := range n.mail {
		if bank == "" || mail.ToBank == bank {
			matched = append(matched, mail)
		}
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	writeJSON(w, http.StatusOK, map[string]any{"mail": matched})
}

func (n *Network) state(w http.ResponseWriter, _ *http.Request) {
	n.mu.Lock()
	defer n.mu.Unlock()
	links := []map[string]string{}
	for _, bank := range banks {
		links = append(links, map[string]string{
			"id": bank.ID, "name": bank.Name, "url": bankURL(bank.ID), "accent": bank.Accent,
			"kifaru_email": bank.KifaruEmail,
		})
	}
	steps := autopilotSteps(n.mode)
	writeJSON(w, http.StatusOK, map[string]any{
		"mode": n.mode, "step": n.step, "busy": n.busy, "steps": steps, "attacker": n.attacker,
		"switch": n.switchLog, "mail": n.mail, "prepared": n.prepared, "banks": links,
		"kifaru_web": kifaruWeb(), "kifaru_api": kifaruAPI(), "customer_password": customerPassword,
	})
}

// setMode resets every bank and switches their Kifaru connectors together.
func (n *Network) setMode(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Mode string `json:"mode"`
	}
	if err := decode(r, &request); err != nil || (request.Mode != "with" && request.Mode != "without") {
		writeError(w, http.StatusBadRequest, "mode must be with or without")
		return
	}
	n.mu.Lock()
	if n.busy {
		n.mu.Unlock()
		writeError(w, http.StatusConflict, "the autopilot is still running a step")
		return
	}
	n.mode, n.salt, n.step = request.Mode, randomID(4), 0
	n.mail, n.switchLog, n.attacker = nil, nil, nil
	salt := n.salt
	n.mu.Unlock()
	failures := []string{}
	for _, bank := range banks {
		if err := postJSON(bankURL(bank.ID)+"/admin/reset", map[string]any{
			"salt": salt, "connector": request.Mode == "with",
		}, nil); err != nil {
			failures = append(failures, bank.Name+": "+err.Error())
		}
	}
	if len(failures) > 0 {
		writeError(w, http.StatusBadGateway, strings.Join(failures, "; "))
		return
	}
	n.state(w, r)
}

type autopilotStep struct {
	Actor string `json:"actor"`
	Title string `json:"title"`
}

func autopilotSteps(mode string) []autopilotStep {
	steps := []autopilotStep{
		{"Attacker", "Take over Grace's NCBA account and send KES 85,000 to Equity 0114417"},
		{"Attacker", "Try Peter's NCBA account from the same phone, KES 120,000"},
		{"Equity Bank", "Equity's fraud desk acts on what it knows"},
		{"Attacker", "Switch to a new phone and take over Faith's KCB account, KES 150,000"},
		{"Attacker", "Cash out everything in the mule account"},
		{"Equity Bank", "Return frozen funds to the sending banks"},
	}
	if mode == "with" {
		steps[2].Title = "Equity's fraud desk places a hold after Kifaru's alert"
	} else {
		steps[2].Title = "Equity's fraud desk checks for anything to act on"
		steps[5].Title = "Equity looks for funds to return"
	}
	return steps
}

func (n *Network) logAttacker(tone, format string, args ...any) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.attacker = append(n.attacker, LogLine{At: time.Now(), Text: fmt.Sprintf(format, args...), Tone: tone})
}

// attackerSession signs in to a bank's customer app the way the attacker's
// browser would, from the given device.
func attackerSession(bank, username, device string) (string, error) {
	var session struct {
		Token string `json:"token"`
	}
	err := postJSON(bankURL(bank)+"/api/login", map[string]string{
		"username": username, "password": customerPassword, "device": device,
	}, &session)
	return session.Token, err
}

func (n *Network) attackTransfer(bank, username, device string, amount float64) error {
	token, err := attackerSession(bank, username, device)
	if err != nil {
		return err
	}
	n.logAttacker("", "signed in to %s as %s (%s)", bankName(bank), username, device)
	var result struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	err = sendJSON(http.MethodPost, bankURL(bank)+"/api/transfers", token, map[string]any{
		"bank": "equity", "account": "0114417", "name": "J. Kiprono", "amount": amount,
	}, &result)
	if err != nil {
		return err
	}
	tone := "ok"
	if result.Status == "blocked" {
		tone = "bad"
	}
	n.logAttacker(tone, "transfer %s to Equity 0114417: %s", kes(amount), result.Status)
	if result.Status == "blocked" {
		n.logAttacker("warn", "%s has flagged this phone. Moving to KCB.", bankName(bank))
	}
	return nil
}

func (n *Network) autopilotNext(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	if n.busy {
		n.mu.Unlock()
		writeError(w, http.StatusConflict, "the autopilot is still running a step")
		return
	}
	steps := autopilotSteps(n.mode)
	if n.step >= len(steps) {
		n.mu.Unlock()
		writeError(w, http.StatusConflict, "the attack is complete; reset to run it again")
		return
	}
	n.busy = true
	step, mode := n.step, n.mode
	n.mu.Unlock()

	err := n.runStep(step, mode)
	n.mu.Lock()
	n.busy = false
	if err == nil {
		n.step++
	}
	n.mu.Unlock()
	if err != nil {
		n.logAttacker("bad", "step failed: %v", err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	n.state(w, r)
}

func (n *Network) runStep(step int, mode string) error {
	switch step {
	case 0:
		return n.attackTransfer("ncba", "grace", "attacker-android", 85000)
	case 1:
		return n.attackTransfer("ncba", "peter", "attacker-android", 120000)
	case 2:
		if mode != "with" {
			n.logAttacker("", "Equity has no alert to act on. NCBA's email sits unread in a shared mailbox.")
			return nil
		}
		if err := waitForAlert(15 * time.Second); err != nil {
			return err
		}
		if err := postJSON(bankURL("equity")+"/api/ops/hold", map[string]string{"account": "0114417"}, nil); err != nil {
			return err
		}
		n.logAttacker("", "Equity's analyst placed a post-no-debit hold on 0114417")
		return nil
	case 3:
		return n.attackTransfer("kcb", "faith", "attacker-iphone", 150000)
	case 4:
		token, err := attackerSession("equity", "kiprono", "attacker-laptop")
		if err != nil {
			return err
		}
		var me struct {
			Balance float64 `json:"balance"`
		}
		if err := sendJSON(http.MethodGet, bankURL("equity")+"/api/me", token, nil, &me); err != nil {
			return err
		}
		n.logAttacker("", "mule balance: %s", kes(me.Balance))
		err = sendJSON(http.MethodPost, bankURL("equity")+"/api/withdraw", token, map[string]any{
			"amount": me.Balance, "channel": "mobile wallets and agents",
		}, nil)
		var declined *httpError
		if errors.As(err, &declined) && declined.status == http.StatusForbidden {
			n.logAttacker("bad", "withdraw %s: declined, account restricted", kes(me.Balance))
			n.logAttacker("warn", "Mule account frozen. Campaign burned.")
			return nil
		}
		if err != nil {
			return err
		}
		n.logAttacker("ok", "withdraw %s to wallets and agents: paid out", kes(me.Balance))
		return nil
	case 5:
		if mode == "with" {
			if err := waitForAlertCount(2, 15*time.Second); err != nil {
				return err
			}
		}
		var result struct {
			Returned float64 `json:"returned"`
		}
		if err := postJSON(bankURL("equity")+"/api/ops/return", map[string]string{"account": "0114417"}, &result); err != nil {
			return err
		}
		if result.Returned == 0 {
			n.logAttacker("", "Equity has nothing left to return. The money is gone.")
		} else {
			n.logAttacker("", "Equity returned %s to NCBA and KCB", kes(result.Returned))
		}
		return nil
	}
	return nil
}

// waitForAlert waits for Equity's connector to pick up a Kifaru alert.
func waitForAlert(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var state struct {
			Alerts []AlertView `json:"alerts"`
		}
		if err := sendJSON(http.MethodGet, bankURL("equity")+"/api/ops/state", "", nil, &state); err == nil &&
			len(state.Alerts) > 0 {
			return nil
		}
		time.Sleep(time.Second)
	}
	return errors.New("Equity has not received a Kifaru alert yet; check that NCBA's report was published")
}

// waitForAlertCount allows asynchronous report validation and receiver polling
// to finish before the presenter records returned funds against every alert.
func waitForAlertCount(count int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var state struct {
			Alerts []AlertView `json:"alerts"`
		}
		if err := sendJSON(http.MethodGet, bankURL("equity")+"/api/ops/state", "", nil, &state); err == nil &&
			len(state.Alerts) >= count {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("Equity received fewer than %d Kifaru alerts before the recovery step", count)
}

// prepare gives NCBA a genuine track record in Kifaru: six earlier campaigns
// that another bank corroborated and whose receiver held the alert. It goes
// through the public API and is safe to run more than once.
func (n *Network) prepare(w http.ResponseWriter, r *http.Request) {
	ncba := newKifaruClient("anthonyjordan@ncba.co.ke", "bank_a")
	abc := newKifaruClient("aminawanjiku@abc.co.ke", "ke:abc-bank-african-banking-corporation")
	absa := newKifaruClient("brianotieno@absa.co.ke", "ke:absa-bank-kenya")
	existing, err := absa.alerts()
	if err != nil {
		writeError(w, http.StatusBadGateway, "Absa alerts: "+err.Error())
		return
	}
	held, adverse := 0, 0
	for _, alert := range existing {
		if alert.ReportingInstitution != "bank_a" {
			continue
		}
		switch alert.Outcome {
		case "held", "recovered":
			held++
		case "released":
			adverse++
		}
		if alert.State == "disputed" && alert.Outcome != "released" {
			adverse++
		}
	}
	// Aim for at least six upheld alerts. If a receiver has released or
	// disputed reports, add enough synthetic holds for good-standing rules.
	target := max(6, 4*adverse)
	if held >= target {
		n.mu.Lock()
		n.prepared = fmt.Sprintf("NCBA already has %d upheld alerts in Kifaru", held)
		n.mu.Unlock()
		n.state(w, r)
		return
	}
	for index := held + 1; index <= target; index++ {
		destination := protect("history", fmt.Sprintf("absa-mule-%d", index))
		report := func(institution, prefix string, confirmed bool) map[string]any {
			// Model the mule's established pattern: it has received from at
			// least five distinct senders. This derives MUL-440, giving each
			// historical report enough evidence to reach the corroborated tier.
			evidence := map[string]any{
				"is_new_device": true, "is_new_beneficiary": true,
				"account_age_days": 3, "distinct_senders_7d": 5,
			}
			if confirmed {
				evidence["victim_confirmed"] = true
			}
			return map[string]any{
				"reporting_institution": institution, "reporting_system": "Online banking fraud engine",
				"transaction_ref":          fmt.Sprintf("%s-HISTORY-SEED-%d", prefix, index),
				"transaction_timestamp":    time.Now().UTC().Add(-time.Duration(index) * 24 * time.Hour).Format(time.RFC3339),
				"subject_account_hash":     protect("history", fmt.Sprintf("%s-victim-%d", prefix, index)),
				"destination_account_hash": destination, "destination_institution": "ke:absa-bank-kenya",
				"amount": 40000 + float64(index)*5000, "currency": "KES", "channel": "instant_payment",
				"bank_risk_score": 0.82, "risk_codes": []string{"ATO-460", "VEL-430"}, "evidence": evidence,
				"narrative": "Earlier synthetic campaign used to give the reporter a track record.",
			}
		}
		first, err := ncba.publish(report("bank_a", "NCBA", true))
		if err != nil {
			writeError(w, http.StatusBadGateway, "NCBA history: "+err.Error())
			return
		}
		if _, err := abc.publish(report("ke:abc-bank-african-banking-corporation", "ABC", false)); err != nil {
			writeError(w, http.StatusBadGateway, "ABC history: "+err.Error())
			return
		}
		alerts, err := absa.alerts()
		if err != nil {
			writeError(w, http.StatusBadGateway, "Absa alerts: "+err.Error())
			return
		}
		matched := false
		for _, alert := range alerts {
			if alert.ReportID == first.ReportID {
				matched = true
				if alert.Outcome == "held" || alert.Outcome == "recovered" {
					held++
				} else {
					if err := absa.decide(alert, "held", "Earlier campaign: account held."); err != nil {
						writeError(w, http.StatusBadGateway, "Absa decision: "+err.Error())
						return
					}
					held++
				}
				break
			}
		}
		if !matched {
			writeError(w, http.StatusBadGateway, "Absa did not receive the prepared report")
			return
		}
	}
	n.mu.Lock()
	n.prepared = fmt.Sprintf("NCBA has %d upheld alerts in Kifaru", held)
	n.mu.Unlock()
	// The director's mutation helper expects a full Network snapshot so it
	// can render immediately without briefly replacing state with {held: n}.
	n.state(w, r)
}
