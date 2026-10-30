package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Test names start with the kind of test they are, so CI can run each kind as
// its own check: TestRegression_, TestSmoke_, TestSecurity_ and TestLoad_.

// unreachable is where every outside service points unless a test starts a
// stub for it. It makes a test that forgets to stub something fail fast
// instead of reaching a demo you have running on the default ports.
const unreachable = "http://127.0.0.1:1"

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("KIFARU_API_URL", unreachable)
	t.Setenv("NETWORK_URL", unreachable)
	t.Setenv("SESSION_IDLE_MINUTES", "")
	for _, bank := range banks {
		t.Setenv(strings.ToUpper(bank.ID)+"_URL", unreachable)
	}
}

// switchStub stands in for the interbank switch and the mail relay, so one
// bank can be tested alone.
type switchStub struct {
	mu        sync.Mutex
	transfers []map[string]any
	returns   []map[string]any
	mail      []map[string]any
	failing   bool
}

func newSwitchStub(t *testing.T) *switchStub {
	t.Helper()
	stub := &switchStub{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		stub.mu.Lock()
		defer stub.mu.Unlock()
		if stub.failing {
			writeError(w, http.StatusBadGateway, "switch down")
			return
		}
		switch r.URL.Path {
		case "/switch/transfer":
			stub.transfers = append(stub.transfers, body)
		case "/switch/return":
			stub.returns = append(stub.returns, body)
		case "/mail":
			stub.mail = append(stub.mail, body)
		default:
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	t.Cleanup(server.Close)
	t.Setenv("NETWORK_URL", server.URL)
	return stub
}

func (s *switchStub) count(kind string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch kind {
	case "transfer":
		return len(s.transfers)
	case "return":
		return len(s.returns)
	}
	return len(s.mail)
}

func startBankConfig(t *testing.T, cfg BankConfig) (*Bank, *httptest.Server) {
	t.Helper()
	bank := newBank(cfg)
	server := httptest.NewServer(bank.routes())
	t.Cleanup(server.Close)
	return bank, server
}

func startBank(t *testing.T, id string) (*Bank, *httptest.Server) {
	t.Helper()
	cfg, ok := bankByID(id)
	if !ok {
		t.Fatalf("unknown bank %q", id)
	}
	return startBankConfig(t, cfg)
}

// api is a small JSON client for the services under test.
type api struct {
	t    *testing.T
	base string
}

func (a api) raw(method, path, token string, body any) (int, []byte, http.Header) {
	a.t.Helper()
	var reader io.Reader
	switch value := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(value)
	case []byte:
		reader = bytes.NewReader(value)
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			a.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, a.base+path, reader)
	if err != nil {
		a.t.Fatal(err)
	}
	if reader != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return response.StatusCode, data, response.Header
}

// call returns the status and the reply decoded as a JSON object (nil when
// the reply is not an object).
func (a api) call(method, path, token string, body any) (int, map[string]any) {
	a.t.Helper()
	status, data, _ := a.raw(method, path, token, body)
	var decoded map[string]any
	_ = json.Unmarshal(data, &decoded)
	return status, decoded
}

func (a api) login(username, device string) string {
	a.t.Helper()
	status, reply := a.call("POST", "/api/login", "", map[string]string{
		"username": username, "password": customerPassword, "device": device,
	})
	if status != http.StatusOK {
		a.t.Fatalf("sign in as %s from %s: status %d %v", username, device, status, reply)
	}
	return reply["token"].(string)
}

func (a api) pay(token, bank, account, name string, amount float64) (int, map[string]any) {
	a.t.Helper()
	return a.call("POST", "/api/transfers", token, map[string]any{
		"bank": bank, "account": account, "name": name, "amount": amount,
	})
}

func (a api) balance(token string) float64 {
	a.t.Helper()
	status, me := a.call("GET", "/api/me", token, nil)
	if status != http.StatusOK {
		a.t.Fatalf("/api/me: status %d %v", status, me)
	}
	return me["balance"].(float64)
}

// transfers returns the bank's own record of its transfers, newest last.
func (a api) transfers() []map[string]any {
	a.t.Helper()
	_, state := a.call("GET", "/api/ops/state", "", nil)
	items, _ := state["transfers"].([]any)
	out := []map[string]any{}
	for _, item := range items {
		out = append(out, item.(map[string]any))
	}
	return out
}

func approx(got, want float64) bool {
	diff := got - want
	return diff > -1e-9 && diff < 1e-9
}

func waitFor(t *testing.T, timeout time.Duration, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (b *Bank) hasEvent(fragment string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, event := range b.events {
		if strings.Contains(event.Text, fragment) {
			return true
		}
	}
	return false
}

// awaitFraudDesk waits for the fraud desk to finish with a blocked transfer,
// so its background work cannot leak into the next test.
func awaitFraudDesk(t *testing.T, bank *Bank) {
	t.Helper()
	waitFor(t, 8*time.Second, "the fraud desk to finish", func() bool {
		return bank.hasEvent("emailed") || bank.hasEvent("Could not email")
	})
}

// world runs all three banks and the network (switch, mailbox and director)
// in-process on free ports, the same code that `go run .` starts.
type world struct {
	t       *testing.T
	banks   map[string]*Bank
	bank    map[string]api
	network *Network
	net     api
}

func newWorld(t *testing.T) *world {
	t.Helper()
	isolate(t)
	w := &world{t: t, banks: map[string]*Bank{}, bank: map[string]api{}}
	for _, cfg := range banks {
		bank, server := startBankConfig(t, cfg)
		w.banks[cfg.ID] = bank
		w.bank[cfg.ID] = api{t, server.URL}
		t.Setenv(strings.ToUpper(cfg.ID)+"_URL", server.URL)
	}
	w.network = newNetwork()
	server := httptest.NewServer(w.network.routes())
	t.Cleanup(server.Close)
	w.net = api{t, server.URL}
	t.Setenv("NETWORK_URL", server.URL)
	return w
}

func (w *world) reset(mode string) {
	w.t.Helper()
	status, reply := w.net.call("POST", "/api/mode", "", map[string]string{"mode": mode})
	if status != http.StatusOK {
		w.t.Fatalf("set mode %s: status %d %v", mode, status, reply)
	}
}

func (w *world) balance(bank, username, device string) float64 {
	w.t.Helper()
	return w.bank[bank].balance(w.bank[bank].login(username, device))
}
