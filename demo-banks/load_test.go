package main

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"sync"
	"testing"
	"time"
)

// Load tests run many customers at once against the real services. They check
// that nothing breaks, that answers stay fast, and that no money is created or
// lost. The thresholds are deliberately loose so a busy CI runner does not
// fail them; they catch a hang, a deadlock or a serious slowdown, not a few
// milliseconds. The speed limits fail the test in CI, or anywhere with
// LOAD_STRICT=1; on a developer machine busy with other work they only log, and
// the correctness checks still fail as usual. For a heavier run against the
// live demo, see load/k6-banks.js.

// strictSpeed reports whether the p95 limits should fail the test.
func strictSpeed() bool { return os.Getenv("CI") != "" || os.Getenv("LOAD_STRICT") != "" }

type sample struct {
	name     string
	status   int
	duration time.Duration
}

type recorder struct {
	mu      sync.Mutex
	samples []sample
}

func (r *recorder) add(name string, status int, started time.Time) {
	r.mu.Lock()
	r.samples = append(r.samples, sample{name, status, time.Since(started)})
	r.mu.Unlock()
}

func (r *recorder) percentile(name string, p float64) time.Duration {
	var all []time.Duration
	for _, s := range r.samples {
		if s.name == name {
			all = append(all, s.duration)
		}
	}
	if len(all) == 0 {
		return 0
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	return all[int(float64(len(all)-1)*p)]
}

func (r *recorder) report(t *testing.T, started time.Time, p95Limit time.Duration) {
	t.Helper()
	names := map[string]int{}
	for _, s := range r.samples {
		names[s.name]++
	}
	keys := []string{}
	for name := range names {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	elapsed := time.Since(started)
	t.Logf("%d requests in %v (%.0f per second)", len(r.samples), elapsed.Round(time.Millisecond), float64(len(r.samples))/elapsed.Seconds())
	for _, name := range keys {
		p95 := r.percentile(name, 0.95)
		t.Logf("  %-22s %5d requests  p50 %-7v p95 %-7v p99 %v", name, names[name],
			r.percentile(name, 0.50).Round(time.Microsecond), p95.Round(time.Microsecond), r.percentile(name, 0.99).Round(time.Microsecond))
		if p95 > p95Limit && strictSpeed() {
			t.Errorf("%s p95 = %v, want under %v", name, p95, p95Limit)
		} else if p95 > p95Limit {
			t.Logf("%s p95 = %v is over the %v limit; set LOAD_STRICT=1 to fail on it here", name, p95, p95Limit)
		}
	}
	for _, s := range r.samples {
		if s.status >= 500 || s.status == 0 {
			t.Errorf("%s answered %d under load", s.name, s.status)
			return
		}
	}
}

func totalMoney(w *world) float64 {
	total := 0.0
	for _, bank := range w.banks {
		bank.mu.Lock()
		for _, account := range bank.accounts {
			total += account.Balance
		}
		bank.mu.Unlock()
	}
	return total
}

// Every customer pays a different account, so no customer trips another's
// fraud rules and the run measures throughput, not lockouts.
var loadPlan = []struct{ bank, user, device, toBank, toAccount string }{
	{"ncba", "grace", "grace-phone", "kcb", "1105108"},
	{"ncba", "peter", "peter-phone", "equity", "0114417"},
	{"kcb", "faith", "faith-phone", "ncba", "1002291"},
	{"equity", "kiprono", "kiprono-phone", "ncba", "1007730"},
}

func TestLoad_ConcurrentCustomersKeepTheLedgerConsistent(t *testing.T) {
	const workersPerCustomer, rounds = 10, 25
	w := newWorld(t)
	w.reset("without")
	// Give the mule account something to spend.
	w.bank["equity"].call("POST", "/interbank/credit", "", map[string]any{"ref": "LOAD-FUND", "from_bank": "ncba", "from_bank_name": "NCBA",
		"from_name": "Load test", "to_account": "0114417", "amount": 500000})
	before := totalMoney(w)

	rec := &recorder{}
	started := time.Now()
	var wg sync.WaitGroup
	var failures sync.Map
	for _, plan := range loadPlan {
		for worker := 0; worker < workersPerCustomer; worker++ {
			wg.Add(1)
			go func(plan struct{ bank, user, device, toBank, toAccount string }) {
				defer wg.Done()
				a := w.bank[plan.bank]
				begin := time.Now()
				status, reply := a.call("POST", "/api/login", "", map[string]string{"username": plan.user, "password": customerPassword, "device": plan.device})
				rec.add("sign in", status, begin)
				if status != http.StatusOK {
					failures.Store(plan.user, fmt.Sprintf("sign in: %d", status))
					return
				}
				token := reply["token"].(string)
				for round := 0; round < rounds; round++ {
					begin = time.Now()
					status, _ = a.call("GET", "/api/me", token, nil)
					rec.add("account (/api/me)", status, begin)
					begin = time.Now()
					status, reply = a.pay(token, plan.toBank, plan.toAccount, "Load test", 10)
					rec.add("payment", status, begin)
					if status != http.StatusOK || reply["status"] != "completed" {
						failures.Store(plan.user, fmt.Sprintf("payment: %d %v", status, reply))
						return
					}
				}
			}(plan)
		}
	}
	wg.Wait()
	rec.report(t, started, 750*time.Millisecond)
	failures.Range(func(user, reason any) bool { t.Errorf("%v: %v", user, reason); return true })

	if after := totalMoney(w); after != before {
		t.Errorf("money in the system changed from %.0f to %.0f: it was created or lost under load", before, after)
	}
	paid := 0
	for _, plan := range loadPlan {
		for _, item := range w.bank[plan.bank].transfers() {
			if item["customer"] == plan.user && item["status"] == "completed" {
				paid++
			}
		}
	}
	if want := len(loadPlan) * workersPerCustomer * rounds; paid != want {
		t.Errorf("%d payments completed, want %d", paid, want)
	}
}

// The ops pages and the director poll every second or two; many open tabs
// must not slow the banks down.
func TestLoad_ManyOpenDashboardsPolling(t *testing.T) {
	const viewers, polls = 40, 30
	w := newWorld(t)
	w.reset("without")
	rec := &recorder{}
	started := time.Now()
	var wg sync.WaitGroup
	for viewer := 0; viewer < viewers; viewer++ {
		wg.Add(1)
		go func(viewer int) {
			defer wg.Done()
			ids := []string{"ncba", "kcb", "equity"}
			for poll := 0; poll < polls; poll++ {
				begin := time.Now()
				status, _ := w.net.call("GET", "/api/state", "", nil)
				rec.add("director state", status, begin)
				begin = time.Now()
				status, _ = w.bank[ids[(viewer+poll)%3]].call("GET", "/api/ops/state", "", nil)
				rec.add("ops state", status, begin)
			}
		}(viewer)
	}
	wg.Wait()
	rec.report(t, started, 500*time.Millisecond)
}

func TestLoad_DirectorSurvivesRapidClicks(t *testing.T) {
	w := newWorld(t)
	w.reset("without")
	var wg sync.WaitGroup
	statuses := make(chan int, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, _, _ := w.net.raw("POST", "/api/autopilot/next", "", map[string]any{})
			statuses <- status
		}()
	}
	wg.Wait()
	close(statuses)
	ok := 0
	for status := range statuses {
		switch status {
		case http.StatusOK:
			ok++
		case http.StatusConflict: // already running a step, or the story is over
		default:
			t.Errorf("rapid clicks produced status %d, want 200 or 409", status)
		}
	}
	_, state := w.net.call("GET", "/api/state", "", nil)
	if step := state["step"].(float64); int(step) != ok {
		t.Errorf("director is at step %v but %d clicks succeeded: steps must run one at a time", step, ok)
	}
	if state["busy"] == true {
		waitFor(t, 10*time.Second, "the running step to finish", func() bool {
			_, s := w.net.call("GET", "/api/state", "", nil)
			return s["busy"] != true
		})
	}
	if ok >= 2 { // step 2 blocks Peter, and NCBA's fraud desk then works in the background
		awaitFraudDesk(t, w.banks["ncba"])
	}
}

func BenchmarkScore(b *testing.B) {
	bank := newBank(banks[0])
	account := bank.accounts["grace"]
	bene := &Beneficiary{Bank: "equity", Account: "0114417", AddedAt: time.Now()}
	for i := 0; i < b.N; i++ {
		bank.score(account, "attacker-android", bene, 85000, nil)
	}
}
