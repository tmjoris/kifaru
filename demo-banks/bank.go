package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Window in which a beneficiary counts as new and a device or beneficiary
// counts as recently shared between customers.
const recentWindow = 60 * time.Minute

type Beneficiary struct {
	Bank     string    `json:"bank"`
	BankName string    `json:"bank_name"`
	Account  string    `json:"account"`
	Name     string    `json:"name"`
	AddedAt  time.Time `json:"added_at"`
}

type Account struct {
	Customer
	Balance       float64        `json:"balance"`
	Hold          bool           `json:"hold"`
	Beneficiaries []*Beneficiary `json:"beneficiaries"`
}

type Publication struct {
	Kind   string       `json:"kind"`
	At     string       `json:"at"`
	Result KifaruResult `json:"result"`
	Error  string       `json:"error,omitempty"`
}

type Transfer struct {
	ID             string       `json:"id"`
	Ref            string       `json:"ref"`
	At             time.Time    `json:"at"`
	Customer       string       `json:"customer"`
	CustomerName   string       `json:"customer_name"`
	Device         string       `json:"device"`
	NewDevice      bool         `json:"new_device"`
	ToBank         string       `json:"to_bank"`
	ToBankName     string       `json:"to_bank_name"`
	ToAccount      string       `json:"to_account"`
	ToName         string       `json:"to_name"`
	Amount         float64      `json:"amount"`
	Score          float64      `json:"score"`
	Threshold      float64      `json:"threshold"`
	Reasons        []string     `json:"reasons"`
	Status         string       `json:"status"`
	FraudConfirmed bool         `json:"fraud_confirmed"`
	Kifaru         *Publication `json:"kifaru,omitempty"`
	codes          []string
	sharedDevice   bool
}

type Credit struct {
	Ref          string    `json:"ref"`
	At           time.Time `json:"at"`
	Account      string    `json:"account"`
	FromBank     string    `json:"from_bank"`
	FromBankName string    `json:"from_bank_name"`
	FromName     string    `json:"from_name"`
	Amount       float64   `json:"amount"`
	State        string    `json:"state"`
}

type Withdrawal struct {
	At      time.Time `json:"at"`
	Account string    `json:"account"`
	Amount  float64   `json:"amount"`
	Channel string    `json:"channel"`
	Status  string    `json:"status"`
}

type Event struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
	Tone string    `json:"tone"`
}

type AlertView struct {
	KifaruAlert
	Account      string `json:"account"`
	ReporterName string `json:"reporter_name"`
	Error        string `json:"error,omitempty"`
}

type session struct {
	username string
	device   string
}

// Bank is one simulated bank: its own customers, ledger, fraud engine and
// Kifaru connector.
type Bank struct {
	cfg    BankConfig
	kifaru *KifaruClient

	mu          sync.Mutex
	salt        string
	connector   bool
	resetAt     time.Time
	accounts    map[string]*Account
	sessions    map[string]session
	deviceUse   map[string]map[string]time.Time
	blocked     map[string]bool
	transfers   []*Transfer
	credits     []*Credit
	withdrawals []*Withdrawal
	events      []Event
	alerts      map[string]*AlertView
	published   []string
}

func newBank(cfg BankConfig) *Bank {
	bank := &Bank{cfg: cfg, kifaru: newKifaruClient(cfg.KifaruEmail, cfg.KifaruCode)}
	bank.resetLocked(randomID(4), false)
	return bank
}

func randomID(size int) string {
	buffer := make([]byte, size)
	_, _ = rand.Read(buffer)
	return hex.EncodeToString(buffer)
}

func (b *Bank) resetLocked(salt string, connector bool) {
	b.salt, b.connector, b.resetAt = salt, connector, time.Now().UTC().Add(-time.Second)
	b.accounts = map[string]*Account{}
	for _, customer := range b.cfg.Customers {
		b.accounts[customer.Username] = &Account{Customer: customer, Balance: customer.Balance}
	}
	b.sessions = map[string]session{}
	b.deviceUse = map[string]map[string]time.Time{}
	b.blocked = map[string]bool{}
	b.transfers, b.credits, b.withdrawals = nil, nil, nil
	b.events = nil
	b.alerts = map[string]*AlertView{}
	b.published = nil
}

func (b *Bank) event(tone, format string, args ...any) {
	b.events = append(b.events, Event{At: time.Now(), Text: fmt.Sprintf(format, args...), Tone: tone})
}

func (b *Bank) accountByNumber(number string) *Account {
	for _, account := range b.accounts {
		if account.Account == number {
			return account
		}
	}
	return nil
}

func bankName(id string) string {
	if bank, ok := bankByID(id); ok {
		return bank.Name
	}
	return id
}

func bankNameForCode(code string) string {
	for _, bank := range banks {
		if bank.KifaruCode == code {
			return bank.Name
		}
	}
	return code
}

func kes(amount float64) string {
	whole := fmt.Sprintf("%.0f", amount)
	var grouped []string
	for len(whole) > 3 {
		grouped = append([]string{whole[len(whole)-3:]}, grouped...)
		whole = whole[:len(whole)-3]
	}
	return "KES " + strings.Join(append([]string{whole}, grouped...), ",")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func decode(r *http.Request, value any) error {
	defer r.Body.Close()
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(value)
}

func (b *Bank) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", page("customer.html"))
	mux.HandleFunc("GET /ops", page("ops.html"))
	mux.Handle("GET /static/", staticFiles())
	mux.HandleFunc("GET /api/config", b.config)
	mux.HandleFunc("POST /api/login", b.login)
	mux.HandleFunc("POST /api/logout", b.logout)
	mux.HandleFunc("GET /api/me", b.me)
	mux.HandleFunc("POST /api/transfers", b.transfer)
	mux.HandleFunc("POST /api/withdraw", b.withdraw)
	mux.HandleFunc("GET /api/ops/state", b.opsState)
	mux.HandleFunc("POST /api/ops/connector", b.setConnector)
	mux.HandleFunc("POST /api/ops/hold", b.hold)
	mux.HandleFunc("POST /api/ops/return", b.returnFunds)
	mux.HandleFunc("POST /admin/reset", b.reset)
	mux.HandleFunc("POST /interbank/credit", b.interbankCredit)
	mux.HandleFunc("POST /interbank/return", b.interbankReturn)
	return mux
}

func (b *Bank) config(w http.ResponseWriter, _ *http.Request) {
	others := []map[string]string{}
	for _, bank := range banks {
		others = append(others, map[string]string{"id": bank.ID, "name": bank.Name})
	}
	customers := []map[string]string{}
	for _, customer := range b.cfg.Customers {
		customers = append(customers, map[string]string{
			"username": customer.Username, "name": customer.Name,
			"account_masked": "•••• " + customer.Account[len(customer.Account)-4:],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": b.cfg.ID, "name": b.cfg.Name, "accent": b.cfg.Accent, "banks": others,
		"customers": customers, "demo_password": customerPassword,
		"kifaru_code": b.cfg.KifaruCode, "network_url": networkURL(),
	})
}

func (b *Bank) login(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Device   string `json:"device"`
	}
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	account := b.accounts[strings.ToLower(strings.TrimSpace(request.Username))]
	if account == nil || account.Password != request.Password {
		writeError(w, http.StatusUnauthorized, "Incorrect username or password.")
		return
	}
	device := strings.TrimSpace(request.Device)
	if device == "" {
		device = "unknown-device"
	}
	if b.blocked[device] {
		b.event("bad", "Refused sign-in to %s's account from a blocked device", account.Name)
		writeError(w, http.StatusForbidden,
			"Sign-in from this device has been blocked by "+b.cfg.Name+"'s fraud team. Please contact customer care.")
		return
	}
	token := randomID(16)
	b.sessions[token] = session{username: account.Username, device: device}
	if b.deviceUse[device] == nil {
		b.deviceUse[device] = map[string]time.Time{}
	}
	b.deviceUse[device][account.Username] = time.Now()
	if !contains(account.KnownDevices, device) {
		b.event("warn", "%s signed in from a device never seen on this account", account.Name)
	} else {
		b.event("", "%s signed in", account.Name)
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token, "name": account.Name})
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func (b *Bank) sessionFor(r *http.Request) (session, *Account, bool) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	current, ok := b.sessions[token]
	if !ok {
		return session{}, nil, false
	}
	return current, b.accounts[current.username], true
}

func (b *Bank) logout(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.sessions, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (b *Bank) me(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, account, ok := b.sessionFor(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "Please sign in again.")
		return
	}
	transfers := []*Transfer{}
	for _, transfer := range b.transfers {
		if transfer.Customer == account.Username {
			transfers = append(transfers, transfer)
		}
	}
	credits := []*Credit{}
	for _, credit := range b.credits {
		if credit.Account == account.Account {
			credits = append(credits, credit)
		}
	}
	withdrawals := []*Withdrawal{}
	for _, withdrawal := range b.withdrawals {
		if withdrawal.Account == account.Account {
			withdrawals = append(withdrawals, withdrawal)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": account.Name, "account": account.Account,
		"account_masked": "•••• " + account.Account[len(account.Account)-4:],
		"customer_ref":   "SIM-" + strings.ToUpper(account.Username), "balance": account.Balance,
		"beneficiaries": account.Beneficiaries, "transfers": transfers, "credits": credits,
		"withdrawals": withdrawals,
	})
}

// score is the bank's own fraud engine. It only sees this bank's customers.
func (b *Bank) score(account *Account, device string, beneficiary *Beneficiary, amount float64) (float64, []string, []string, bool) {
	now := time.Now()
	score := 0.10
	reasons, codes := []string{}, []string{}
	newDevice := !contains(account.KnownDevices, device)
	newBeneficiary := now.Sub(beneficiary.AddedAt) < recentWindow
	if newDevice {
		score += 0.25
		reasons = append(reasons, "New device")
	}
	if newBeneficiary {
		score += 0.20
		reasons = append(reasons, "New beneficiary")
	}
	if newDevice && newBeneficiary {
		codes = append(codes, "ATO-460")
	}
	if newBeneficiary {
		codes = append(codes, "VEL-430")
		reasons = append(reasons, "Beneficiary added and used in the same session")
	}
	if amount >= 50000 {
		score += 0.05
		reasons = append(reasons, "Large amount")
	}
	shared := false
	for username, seen := range b.deviceUse[device] {
		if username != account.Username && now.Sub(seen) < recentWindow {
			shared = true
		}
	}
	if shared {
		score += 0.35
		reasons = append(reasons, "Device used on another customer's account")
		codes = append(codes, "IP-401")
	}
	for _, transfer := range b.transfers {
		if transfer.Customer != account.Username && transfer.ToBank == beneficiary.Bank &&
			transfer.ToAccount == beneficiary.Account && now.Sub(transfer.At) < recentWindow {
			score += 0.25
			reasons = append(reasons, "Beneficiary just paid by another customer")
			break
		}
	}
	if newBeneficiary {
		for _, transfer := range b.transfers {
			if transfer.Customer == account.Username && transfer.Status == "completed" &&
				transfer.ToBank == beneficiary.Bank && transfer.ToAccount == beneficiary.Account {
				score += 0.25
				reasons = append(reasons, "Repeat payment to a beneficiary added this session")
				break
			}
		}
	}
	return math.Min(0.99, math.Round(score*100)/100), reasons, codes, shared
}

func (b *Bank) transfer(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Bank    string  `json:"bank"`
		Account string  `json:"account"`
		Name    string  `json:"name"`
		Amount  float64 `json:"amount"`
	}
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	b.mu.Lock()
	current, account, ok := b.sessionFor(r)
	if !ok {
		b.mu.Unlock()
		writeError(w, http.StatusUnauthorized, "Please sign in again.")
		return
	}
	request.Account = strings.Join(strings.Fields(request.Account), "")
	if _, known := bankByID(request.Bank); !known || request.Account == "" {
		b.mu.Unlock()
		writeError(w, http.StatusBadRequest, "Choose a bank and enter an account number.")
		return
	}
	if request.Amount <= 0 || request.Amount > account.Balance {
		b.mu.Unlock()
		writeError(w, http.StatusBadRequest, "Enter an amount up to your available balance.")
		return
	}
	if account.Hold {
		b.mu.Unlock()
		writeError(w, http.StatusForbidden, "This account is restricted. Please contact your branch.")
		return
	}
	var beneficiary *Beneficiary
	for _, existing := range account.Beneficiaries {
		if existing.Bank == request.Bank && existing.Account == request.Account {
			beneficiary = existing
		}
	}
	if beneficiary == nil {
		name := strings.TrimSpace(request.Name)
		if name == "" {
			name = "Account " + request.Account
		}
		beneficiary = &Beneficiary{Bank: request.Bank, BankName: bankName(request.Bank),
			Account: request.Account, Name: name, AddedAt: time.Now()}
		account.Beneficiaries = append(account.Beneficiaries, beneficiary)
		b.event("", "%s added a new beneficiary: %s %s", account.Name, beneficiary.BankName, beneficiary.Account)
	}
	score, reasons, codes, shared := b.score(account, current.device, beneficiary, request.Amount)
	transfer := &Transfer{
		ID: randomID(3), At: time.Now(), Customer: account.Username, CustomerName: account.Name,
		Device: current.device, NewDevice: !contains(account.KnownDevices, current.device),
		ToBank: beneficiary.Bank, ToBankName: beneficiary.BankName, ToAccount: beneficiary.Account,
		ToName: beneficiary.Name, Amount: request.Amount, Score: score, Threshold: b.cfg.BlockThreshold,
		Reasons: reasons, codes: codes, sharedDevice: shared,
	}
	transfer.Ref = strings.ToUpper(b.cfg.ID) + "-" + transfer.ID
	b.transfers = append(b.transfers, transfer)

	if score >= b.cfg.BlockThreshold {
		transfer.Status = "blocked"
		b.event("bad", "Blocked %s from %s to %s %s (score %.2f)", kes(transfer.Amount),
			account.Name, transfer.ToBankName, transfer.ToAccount, score)
		b.lockOutDevice(current.device)
		b.mu.Unlock()
		go b.investigate(transfer)
		writeJSON(w, http.StatusOK, map[string]any{"status": "blocked",
			"message": "This transfer has been blocked for your security and you have been signed out. Please contact customer care."})
		return
	}
	account.Balance -= transfer.Amount
	b.mu.Unlock()

	err := postJSON(networkURL()+"/switch/transfer", map[string]any{
		"ref": transfer.Ref, "from_bank": b.cfg.ID, "from_bank_name": b.cfg.Name,
		"from_name": account.Name, "to_bank": transfer.ToBank, "to_account": transfer.ToAccount,
		"amount": transfer.Amount,
	}, nil)

	b.mu.Lock()
	if err != nil {
		account.Balance += transfer.Amount
		transfer.Status = "failed"
		b.event("bad", "Transfer %s failed at the interbank switch: %v", transfer.Ref, err)
		b.mu.Unlock()
		writeError(w, http.StatusBadGateway, "The transfer could not be completed. Please try again.")
		return
	}
	transfer.Status = "completed"
	b.event("ok", "Sent %s from %s to %s %s (score %.2f, below %.2f)", kes(transfer.Amount),
		account.Name, transfer.ToBankName, transfer.ToAccount, score, b.cfg.BlockThreshold)
	publish := b.connector && score >= b.cfg.ReportThreshold
	b.mu.Unlock()
	if publish {
		go b.publish(transfer, "flagged transfer", 0)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "completed",
		"message": fmt.Sprintf("%s sent to %s %s.", kes(transfer.Amount), transfer.ToBankName, transfer.ToAccount)})
}

// lockOutDevice ends every session on a device that made a blocked transfer
// and refuses its further sign-ins until the next reset. Callers hold b.mu.
func (b *Bank) lockOutDevice(device string) {
	b.blocked[device] = true
	ended := 0
	for token, current := range b.sessions {
		if current.device == device {
			delete(b.sessions, token)
			ended++
		}
	}
	b.event("bad", "Fraud team signed out %d session(s) on the device and blocked its sign-ins", ended)
}

// investigate is the fraud desk's follow-up to a blocked transfer: it calls
// customers, including the account holder, who recently paid the same
// beneficiary.
func (b *Bank) investigate(blocked *Transfer) {
	b.mu.Lock()
	linked := []*Transfer{}
	for _, transfer := range b.transfers {
		if transfer.Status == "completed" && !transfer.FraudConfirmed &&
			transfer.ToBank == blocked.ToBank && transfer.ToAccount == blocked.ToAccount &&
			blocked.At.Sub(transfer.At) < recentWindow {
			linked = append(linked, transfer)
			b.event("warn", "Fraud desk is calling %s about %s sent to %s %s", transfer.CustomerName,
				kes(transfer.Amount), transfer.ToBankName, transfer.ToAccount)
		}
	}
	connector := b.connector
	b.mu.Unlock()

	if len(linked) == 0 {
		if connector {
			b.publish(blocked, "blocked attempt", 0)
		} else {
			b.email(blocked, false)
		}
		return
	}
	time.Sleep(2500 * time.Millisecond)
	for _, transfer := range linked {
		b.mu.Lock()
		transfer.FraudConfirmed = true
		b.event("bad", "%s confirms they did not make the %s transfer", transfer.CustomerName, kes(transfer.Amount))
		connector = b.connector
		b.mu.Unlock()
		if connector {
			b.publish(transfer, "customer-confirmed fraud", 1)
		} else {
			b.email(transfer, true)
		}
	}
}

func (b *Bank) email(transfer *Transfer, confirmed bool) {
	detail := "blocked a transfer to"
	if confirmed {
		detail = fmt.Sprintf("confirmed fraud on %s sent to", kes(transfer.Amount))
	}
	err := postJSON(networkURL()+"/mail", map[string]any{
		"from_bank": b.cfg.ID, "from_name": b.cfg.Name + " fraud team", "to_bank": transfer.ToBank,
		"subject": "Suspected mule account " + transfer.ToAccount,
		"body": fmt.Sprintf("Hello, %s has %s your account %s. Could you look into it? Regards, %s fraud team.",
			b.cfg.Name, detail, transfer.ToAccount, b.cfg.Name),
	}, nil)
	b.mu.Lock()
	defer b.mu.Unlock()
	if err != nil {
		b.event("bad", "Could not email %s: %v", transfer.ToBankName, err)
		return
	}
	b.event("", "No Kifaru connector: emailed %s's fraud mailbox about account %s", transfer.ToBankName, transfer.ToAccount)
}

// publish sends a protected report to Kifaru. Only hashed identifiers leave
// the bank.
func (b *Bank) publish(transfer *Transfer, kind string, blockedAttempts int) {
	b.mu.Lock()
	destination, _ := bankByID(transfer.ToBank)
	account := b.accounts[transfer.Customer]
	codes := append([]string{}, transfer.codes...)
	if len(codes) == 0 {
		codes = []string{"ATO-460"}
	}
	evidence := map[string]any{
		"is_new_device":       transfer.NewDevice,
		"is_new_beneficiary":  true,
		"device_profile":      protect(b.salt, transfer.Device),
		"synthetic_bank_demo": true,
		"demo_event_type":     "bank_transfer_risk_signal",
	}
	if transfer.FraudConfirmed {
		evidence["victim_confirmed"] = true
	}
	if blockedAttempts > 0 {
		evidence["blocked_followup_attempts"] = blockedAttempts
	}
	report := map[string]any{
		"reporting_institution":    b.cfg.KifaruCode,
		"reporting_system":         b.cfg.Name + " online banking fraud engine",
		"transaction_ref":          fmt.Sprintf("%s-%s-%s", strings.ToUpper(b.cfg.ID), b.salt, transfer.ID),
		"transaction_timestamp":    transfer.At.UTC().Format(time.RFC3339),
		"subject_account_hash":     protect(b.salt, account.Account),
		"subject_customer_hash":    protect(b.salt, b.cfg.ID+":"+account.Username),
		"destination_account_hash": protect(b.salt, transfer.ToAccount),
		"destination_institution":  destination.KifaruCode,
		"amount":                   transfer.Amount,
		"currency":                 "KES",
		"channel":                  "instant_payment",
		"bank_risk_score":          transfer.Score,
		"risk_codes":               codes,
		"evidence":                 evidence,
		"narrative":                fmt.Sprintf("%s: %s.", b.cfg.Name, kind),
	}
	b.mu.Unlock()

	result, err := b.kifaru.publish(report)
	b.mu.Lock()
	defer b.mu.Unlock()
	publication := &Publication{Kind: kind, At: time.Now().Format("15:04:05"), Result: result}
	transfer.Kifaru = publication
	if err != nil {
		publication.Error = err.Error()
		b.event("bad", "Kifaru connector error: %v", err)
		return
	}
	b.published = append(b.published, result.ReportID)
	b.event("kifaru", "Published %s to Kifaru as %s: %s", kind, result.ReportID, describeStatus(result.Status))
}

func describeStatus(status string) string {
	switch status {
	case "CORROBORATED_SIGNAL":
		return "corroborated, alert sent to the receiving bank"
	case "SINGLE_SOURCE_REVIEW":
		return "single-source review sent to the receiving bank"
	case "SINGLE_SOURCE_NOTICE":
		return "single-source notice sent to the receiving bank"
	case "AWAITING_CORROBORATION":
		return "awaiting corroboration from another institution"
	}
	return strings.ToLower(strings.ReplaceAll(status, "_", " "))
}

func (b *Bank) withdraw(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Amount  float64 `json:"amount"`
		Channel string  `json:"channel"`
	}
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	_, account, ok := b.sessionFor(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "Please sign in again.")
		return
	}
	if request.Amount <= 0 || request.Amount > account.Balance {
		writeError(w, http.StatusBadRequest, "Enter an amount up to your available balance.")
		return
	}
	channel := strings.TrimSpace(request.Channel)
	if channel == "" {
		channel = "mobile wallets and agents"
	}
	withdrawal := &Withdrawal{At: time.Now(), Account: account.Account, Amount: request.Amount, Channel: channel}
	b.withdrawals = append(b.withdrawals, withdrawal)
	if account.Hold {
		withdrawal.Status = "declined"
		b.event("ok", "Declined a %s withdrawal from %s: the account is on hold", kes(request.Amount), account.Account)
		writeJSON(w, http.StatusForbidden, map[string]string{"status": "declined",
			"error": "Withdrawal declined. This account is restricted. Please visit a branch."})
		return
	}
	withdrawal.Status = "paid out"
	account.Balance -= request.Amount
	remaining := request.Amount
	for _, credit := range b.credits {
		if credit.Account == account.Account && credit.State == "available" && remaining > 0 {
			credit.State = "withdrawn"
			remaining -= credit.Amount
		}
	}
	b.event("bad", "Paid out %s from %s to %s", kes(request.Amount), account.Account, channel)
	writeJSON(w, http.StatusOK, map[string]string{"status": "paid out",
		"message": fmt.Sprintf("%s sent to %s.", kes(request.Amount), channel)})
}

func (b *Bank) interbankCredit(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Ref          string  `json:"ref"`
		FromBank     string  `json:"from_bank"`
		FromBankName string  `json:"from_bank_name"`
		FromName     string  `json:"from_name"`
		ToAccount    string  `json:"to_account"`
		Amount       float64 `json:"amount"`
	}
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	account := b.accountByNumber(request.ToAccount)
	if account == nil {
		writeError(w, http.StatusNotFound, "no such account")
		return
	}
	account.Balance += request.Amount
	credit := &Credit{Ref: request.Ref, At: time.Now(), Account: account.Account, FromBank: request.FromBank,
		FromBankName: request.FromBankName, FromName: request.FromName, Amount: request.Amount, State: "available"}
	if account.Hold {
		credit.State = "frozen"
		b.event("ok", "Received %s from %s into held account %s: frozen on arrival", kes(request.Amount),
			request.FromBankName, account.Account)
	} else {
		b.event("", "Received %s from %s (%s) into %s", kes(request.Amount), request.FromBankName,
			request.FromName, account.Account)
	}
	b.credits = append(b.credits, credit)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (b *Bank) interbankReturn(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Ref          string  `json:"ref"`
		Amount       float64 `json:"amount"`
		ReturnedBy   string  `json:"returned_by"`
		ReturnedName string  `json:"returned_by_name"`
	}
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, transfer := range b.transfers {
		if transfer.Ref == request.Ref && transfer.Status == "completed" {
			transfer.Status = "returned"
			b.accounts[transfer.Customer].Balance += request.Amount
			b.event("ok", "%s returned %s to %s", request.ReturnedName, kes(request.Amount), transfer.CustomerName)
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
	}
	writeError(w, http.StatusNotFound, "no completed transfer with that reference")
}

func (b *Bank) opsState(w http.ResponseWriter, _ *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	accounts := []*Account{}
	for _, account := range b.accounts {
		accounts = append(accounts, account)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].Account < accounts[j].Account })
	alerts := []*AlertView{}
	for _, alert := range b.alerts {
		alerts = append(alerts, alert)
	}
	sort.Slice(alerts, func(i, j int) bool { return alerts[i].IssuedAt < alerts[j].IssuedAt })
	events := b.events
	if len(events) > 60 {
		events = events[len(events)-60:]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": b.cfg.ID, "name": b.cfg.Name, "accent": b.cfg.Accent, "kifaru_code": b.cfg.KifaruCode,
		"kifaru_email": b.cfg.KifaruEmail, "connector": b.connector, "block_threshold": b.cfg.BlockThreshold,
		"report_threshold": b.cfg.ReportThreshold, "accounts": accounts, "transfers": b.transfers,
		"credits": b.credits, "withdrawals": b.withdrawals, "events": events, "alerts": alerts,
		"receives": b.cfg.ID == "equity", "network_url": networkURL(),
	})
}

func (b *Bank) setConnector(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Enabled bool `json:"enabled"`
	}
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.connector = request.Enabled
	if request.Enabled {
		b.event("kifaru", "Kifaru connector switched on")
	} else {
		b.event("", "Kifaru connector switched off")
	}
	writeJSON(w, http.StatusOK, map[string]bool{"connector": b.connector})
}

func (b *Bank) reset(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Salt      string `json:"salt"`
		Connector bool   `json:"connector"`
	}
	if err := decode(r, &request); err != nil || request.Salt == "" {
		writeError(w, http.StatusBadRequest, "salt is required")
		return
	}
	b.mu.Lock()
	previous := b.published
	b.resetLocked(request.Salt, request.Connector)
	b.mu.Unlock()
	// Retract before acknowledging reset. The director resets banks one at a
	// time; background retractions from several banks can race Kifaru's linked
	// report revalidation and deadlock its database transactions.
	for _, reportID := range previous {
		if err := b.kifaru.retract(reportID, "Demo reset"); err != nil {
			log.Printf("%s: retract %s: %v", b.cfg.ID, reportID, err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// pollAlerts keeps the receiving bank's view of its Kifaru alerts current.
func (b *Bank) pollAlerts() {
	for range time.Tick(2 * time.Second) {
		b.mu.Lock()
		connector, salt := b.connector, b.salt
		since := b.resetAt.Format("2006-01-02T15:04:05Z")
		hashes := map[string]string{}
		for _, account := range b.accounts {
			hashes[protect(salt, account.Account)] = account.Account
		}
		b.mu.Unlock()
		if !connector {
			continue
		}
		alerts, err := b.kifaru.alerts()
		if err != nil {
			log.Printf("%s: poll alerts: %v", b.cfg.ID, err)
			continue
		}
		b.mu.Lock()
		if b.salt == salt {
			for _, alert := range alerts {
				account, ours := hashes[alert.DestinationAccountHash]
				if !ours || alert.IssuedAt < since {
					continue
				}
				view, seen := b.alerts[alert.AlertID]
				if !seen {
					view = &AlertView{Account: account, ReporterName: bankNameForCode(alert.ReportingInstitution)}
					b.alerts[alert.AlertID] = view
					b.event("kifaru", "Kifaru alert: %s from %s about account %s", tierName(alert.Tier),
						view.ReporterName, account)
				} else if view.Tier != alert.Tier {
					b.event("kifaru", "Kifaru escalated the %s alert to %s", view.ReporterName, tierName(alert.Tier))
				}
				view.KifaruAlert = alert
			}
		}
		b.mu.Unlock()
	}
}

func tierName(tier string) string {
	switch tier {
	case "corroborated":
		return "corroborated alert"
	case "single_source_review":
		return "single-source review"
	case "single_source_notice":
		return "single-source notice"
	}
	return tier
}

func (b *Bank) hold(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Account string `json:"account"`
	}
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	b.mu.Lock()
	account := b.accountByNumber(request.Account)
	if account == nil {
		b.mu.Unlock()
		writeError(w, http.StatusNotFound, "no such account")
		return
	}
	account.Hold = true
	for _, credit := range b.credits {
		if credit.Account == account.Account && credit.State == "available" {
			credit.State = "frozen"
		}
	}
	b.event("ok", "Post-no-debit hold placed on %s: money can arrive but cannot leave", account.Account)
	pending := b.alertsFor(account.Account, func(alert *AlertView) bool { return alert.State != "actioned" })
	b.mu.Unlock()
	b.decideAlerts(pending, "held", "Post-no-debit hold placed on the beneficiary account.")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (b *Bank) returnFunds(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Account string `json:"account"`
	}
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	b.mu.Lock()
	account := b.accountByNumber(request.Account)
	if account == nil {
		b.mu.Unlock()
		writeError(w, http.StatusNotFound, "no such account")
		return
	}
	returnable := []*Credit{}
	for _, credit := range b.credits {
		if credit.Account == account.Account && (credit.State == "frozen" || credit.State == "available") {
			returnable = append(returnable, credit)
		}
	}
	b.mu.Unlock()
	returned := 0.0
	for _, credit := range returnable {
		err := postJSON(networkURL()+"/switch/return", map[string]any{
			"ref": credit.Ref, "to_bank": credit.FromBank, "amount": credit.Amount,
			"returned_by": b.cfg.ID, "returned_by_name": b.cfg.Name,
		}, nil)
		b.mu.Lock()
		if err != nil {
			b.event("bad", "Could not return %s to %s: %v", kes(credit.Amount), credit.FromBankName, err)
		} else {
			credit.State = "returned"
			account.Balance -= credit.Amount
			returned += credit.Amount
			b.event("ok", "Returned %s to %s", kes(credit.Amount), credit.FromBankName)
		}
		b.mu.Unlock()
	}
	b.mu.Lock()
	recoverable := b.alertsFor(account.Account, func(alert *AlertView) bool { return alert.Outcome != "recovered" })
	b.mu.Unlock()
	if returned > 0 {
		b.decideAlerts(recoverable, "recovered", "Frozen funds returned to the sending banks.")
	}
	writeJSON(w, http.StatusOK, map[string]any{"returned": returned})
}

func (b *Bank) alertsFor(account string, include func(*AlertView) bool) []KifaruAlert {
	matched := []KifaruAlert{}
	for _, alert := range b.alerts {
		if alert.Account == account && alert.State != "retracted" && alert.State != "disputed" && include(alert) {
			matched = append(matched, alert.KifaruAlert)
		}
	}
	return matched
}

// decideAlerts records the receiver's decision in Kifaru, which feeds back
// to the reporting banks and their standing.
func (b *Bank) decideAlerts(alerts []KifaruAlert, outcome, comment string) {
	for _, alert := range alerts {
		err := b.kifaru.decide(alert, outcome, comment)
		b.mu.Lock()
		if view := b.alerts[alert.AlertID]; view != nil {
			if err != nil {
				view.Error = err.Error()
				b.event("bad", "Could not record %s in Kifaru: %v", outcome, err)
			} else {
				view.Error = ""
				view.State, view.Outcome = "actioned", outcome
				b.event("kifaru", "Recorded %s on the %s alert in Kifaru", outcome, view.ReporterName)
			}
		}
		b.mu.Unlock()
	}
}
