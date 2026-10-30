package main

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// scoreCase is one row of the score table in TestWork.md. setup changes the
// starting state: Grace on a known device paying an old, known beneficiary
// KES 1,000, which scores the 0.10 base.
type scoreCase struct {
	name   string
	amount float64
	device string
	setup  func(b *Bank, a *Account, bene *Beneficiary)
	risk   *RiskCheck
	want   float64
}

func scoreFixture() (*Bank, *Account, *Beneficiary) {
	b := newBank(banks[0])
	bene := &Beneficiary{Bank: "equity", BankName: "Equity Bank", Account: "0114417", Name: "J. Kiprono",
		AddedAt: time.Now().Add(-2 * time.Hour)}
	return b, b.accounts["grace"], bene
}

func flagged(tier string) *RiskCheck {
	return &RiskCheck{Destination: RiskSignal{Checked: true, Flagged: true, Tier: tier, Institutions: 1}}
}

func TestRegression_ScoreTable(t *testing.T) {
	justNow := func() time.Time { return time.Now().Add(-5 * time.Minute) }
	cases := []scoreCase{
		{name: "base", amount: 1000, device: "grace-phone", want: 0.10},
		{name: "new device", amount: 1000, device: "new-phone", want: 0.35},
		{name: "new beneficiary", amount: 1000, device: "grace-phone", want: 0.30,
			setup: func(_ *Bank, _ *Account, bene *Beneficiary) { bene.AddedAt = time.Now() }},
		{name: "amount just under KES 50,000", amount: 49999, device: "grace-phone", want: 0.10},
		{name: "amount of KES 50,000", amount: 50000, device: "grace-phone", want: 0.15},
		{name: "new device, new beneficiary, large amount", amount: 85000, device: "attacker-android", want: 0.60,
			setup: func(_ *Bank, _ *Account, bene *Beneficiary) { bene.AddedAt = time.Now() }},
		{name: "device used on another customer's account in the hour", amount: 1000, device: "grace-phone", want: 0.45,
			setup: func(b *Bank, _ *Account, _ *Beneficiary) {
				b.deviceUse["grace-phone"] = map[string]time.Time{"peter": justNow()}
			}},
		{name: "device used on another account over an hour ago", amount: 1000, device: "grace-phone", want: 0.10,
			setup: func(b *Bank, _ *Account, _ *Beneficiary) {
				b.deviceUse["grace-phone"] = map[string]time.Time{"peter": time.Now().Add(-2 * time.Hour)}
			}},
		{name: "device used only by the same customer", amount: 1000, device: "grace-phone", want: 0.10,
			setup: func(b *Bank, _ *Account, _ *Beneficiary) {
				b.deviceUse["grace-phone"] = map[string]time.Time{"grace": justNow()}
			}},
		{name: "beneficiary just paid by another customer", amount: 1000, device: "grace-phone", want: 0.35,
			setup: func(b *Bank, _ *Account, bene *Beneficiary) {
				b.transfers = append(b.transfers, &Transfer{Customer: "peter", ToBank: bene.Bank, ToAccount: bene.Account,
					At: justNow(), Status: "completed"})
			}},
		{name: "beneficiary paid by another customer over an hour ago", amount: 1000, device: "grace-phone", want: 0.10,
			setup: func(b *Bank, _ *Account, bene *Beneficiary) {
				b.transfers = append(b.transfers, &Transfer{Customer: "peter", ToBank: bene.Bank, ToAccount: bene.Account,
					At: time.Now().Add(-2 * time.Hour), Status: "completed"})
			}},
		{name: "repeat payment to a beneficiary added this session", amount: 1000, device: "grace-phone", want: 0.55,
			setup: func(b *Bank, _ *Account, bene *Beneficiary) {
				bene.AddedAt = time.Now()
				b.transfers = append(b.transfers, &Transfer{Customer: "grace", ToBank: bene.Bank, ToAccount: bene.Account,
					At: justNow(), Status: "completed"})
			}},
		{name: "repeat payment does not count when the first was blocked", amount: 1000, device: "grace-phone", want: 0.30,
			setup: func(b *Bank, _ *Account, bene *Beneficiary) {
				bene.AddedAt = time.Now()
				b.transfers = append(b.transfers, &Transfer{Customer: "grace", ToBank: bene.Bank, ToAccount: bene.Account,
					At: justNow(), Status: "blocked"})
			}},
		{name: "Kifaru: device reported by another institution", amount: 1000, device: "grace-phone", want: 0.45,
			risk: &RiskCheck{Device: RiskSignal{Checked: true, Flagged: true, Tier: "corroborated", Institutions: 1}}},
		{name: "Kifaru: device checked but not flagged", amount: 1000, device: "grace-phone", want: 0.10,
			risk: &RiskCheck{Device: RiskSignal{Checked: true}, Destination: RiskSignal{Checked: true}}},
		{name: "Kifaru: beneficiary corroborated", amount: 1000, device: "grace-phone", want: 0.50, risk: flagged("corroborated")},
		{name: "Kifaru: beneficiary single-source review", amount: 1000, device: "grace-phone", want: 0.50, risk: flagged("single_source_review")},
		{name: "Kifaru: beneficiary single-source notice", amount: 1000, device: "grace-phone", want: 0.35, risk: flagged("single_source_notice")},
		{name: "Kifaru: beneficiary on the known-risk list", amount: 1000, device: "grace-phone", want: 0.35, risk: flagged("known_risk")},
		{name: "Kifaru: beneficiary reported, awaiting corroboration", amount: 1000, device: "grace-phone", want: 0.30, risk: flagged("reported")},
		{name: "score is capped at 0.99", amount: 120000, device: "attacker-android", want: 0.99,
			setup: func(b *Bank, _ *Account, bene *Beneficiary) {
				bene.AddedAt = time.Now()
				b.deviceUse["attacker-android"] = map[string]time.Time{"peter": justNow()}
			},
			risk: &RiskCheck{Device: RiskSignal{Flagged: true}, Destination: RiskSignal{Flagged: true, Tier: "corroborated"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, account, bene := scoreFixture()
			if tc.setup != nil {
				tc.setup(b, account, bene)
			}
			got, reasons, _, _ := b.score(account, tc.device, bene, tc.amount, tc.risk)
			if !approx(got, tc.want) {
				t.Fatalf("score = %.2f, want %.2f (reasons %v)", got, tc.want, reasons)
			}
		})
	}
}

func TestRegression_ScoreExplainsItself(t *testing.T) {
	b, account, bene := scoreFixture()
	bene.AddedAt = time.Now()
	score, reasons, codes, _ := b.score(account, "attacker-android", bene, 85000, nil)
	if !approx(score, 0.60) {
		t.Fatalf("score = %.2f, want 0.60", score)
	}
	for _, want := range []string{"New device", "New beneficiary", "Large amount"} {
		if !contains(reasons, want) {
			t.Errorf("reasons %v are missing %q", reasons, want)
		}
	}
	for _, want := range []string{"ATO-460", "VEL-430"} {
		if !contains(codes, want) {
			t.Errorf("risk codes %v are missing %q", codes, want)
		}
	}
}

func TestRegression_BankThresholds(t *testing.T) {
	want := map[string][2]float64{"ncba": {0.80, 0.70}, "kcb": {0.75, 0.45}, "equity": {0.80, 0.70}}
	for _, bank := range banks {
		lines, ok := want[bank.ID]
		if !ok {
			t.Fatalf("unexpected bank %s", bank.ID)
		}
		if !approx(bank.BlockThreshold, lines[0]) || !approx(bank.ReportThreshold, lines[1]) {
			t.Errorf("%s: block %.2f / report %.2f, want %.2f / %.2f", bank.ID,
				bank.BlockThreshold, bank.ReportThreshold, lines[0], lines[1])
		}
	}
}

// The block line is inclusive: a transfer that scores exactly the threshold
// is blocked, and one that scores 0.01 below it goes through.
func TestRegression_BlockLineIsInclusive(t *testing.T) {
	for _, tc := range []struct {
		threshold float64
		status    string
	}{{0.60, "blocked"}, {0.61, "completed"}} {
		t.Run(fmt.Sprintf("threshold %.2f", tc.threshold), func(t *testing.T) {
			isolate(t)
			newSwitchStub(t)
			cfg, _ := bankByID("ncba")
			cfg.BlockThreshold = tc.threshold
			bank, server := startBankConfig(t, cfg)
			a := api{t, server.URL}
			token := a.login("grace", "attacker-android")
			_, reply := a.pay(token, "equity", "0114417", "J. Kiprono", 85000)
			if reply["status"] != tc.status {
				t.Fatalf("a payment scoring 0.60 against a %.2f line: status %v, want %s", tc.threshold, reply["status"], tc.status)
			}
			if tc.status == "blocked" {
				awaitFraudDesk(t, bank)
			}
		})
	}
}

func TestRegression_ReportLineDecidesWhatIsSentToKifaru(t *testing.T) {
	// KCB reports at 0.45 and blocks at 0.75. A 0.60 payment completes and is reported.
	// NCBA reports at 0.70, so the same payment is not reported there.
	for _, tc := range []struct {
		bank   string
		report bool
	}{{"kcb", true}, {"ncba", false}} {
		t.Run(tc.bank, func(t *testing.T) {
			isolate(t)
			newSwitchStub(t)
			kifaru := newKifaruStub(t)
			_, server := startBank(t, tc.bank)
			a := api{t, server.URL}
			if status, _ := a.call("POST", "/api/ops/connector", "", map[string]bool{"enabled": true}); status != http.StatusOK {
				t.Fatalf("connector toggle: status %d", status)
			}
			user := map[string]string{"kcb": "faith", "ncba": "grace"}[tc.bank]
			token := a.login(user, "attacker-phone")
			_, reply := a.pay(token, "equity", "0114417", "J. Kiprono", 150000)
			if reply["status"] != "completed" {
				t.Fatalf("status = %v, want completed", reply["status"])
			}
			if tc.report {
				waitFor(t, 3*time.Second, "the report to reach Kifaru", func() bool { return kifaru.count("/v1/reports") == 1 })
			} else {
				time.Sleep(300 * time.Millisecond)
				if n := kifaru.count("/v1/reports"); n != 0 {
					t.Fatalf("%d reports sent, want none below the report line", n)
				}
			}
		})
	}
}
