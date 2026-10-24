package main

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Customer is an online banking customer. Every customer and account is
// synthetic.
type Customer struct {
	Username      string
	Password      string
	Name          string
	Account       string
	Balance       float64
	KnownDevices  []string
	OpenedDaysAgo int
}

// BankConfig describes one simulated bank. Each bank runs as its own server
// with its own state and fraud rules, and reaches the others only through
// the interbank switch and Kifaru's public API.
type BankConfig struct {
	ID              string
	Name            string
	Port            string
	Accent          string
	KifaruCode      string
	KifaruEmail     string
	BlockThreshold  float64
	ReportThreshold float64
	Customers       []Customer
}

const customerPassword = "bank1234"

var banks = []BankConfig{
	{
		ID: "ncba", Name: "NCBA", Port: env("NCBA_PORT", "9101"), Accent: "#493a32",
		KifaruCode: "bank_a", KifaruEmail: "anthonyjordan@ncba.co.ke",
		BlockThreshold: 0.80, ReportThreshold: 0.70,
		Customers: []Customer{
			{Username: "grace", Password: customerPassword, Name: "Grace Achieng", Account: "1002291",
				Balance: 412500, KnownDevices: []string{"grace-phone"}, OpenedDaysAgo: 2900},
			{Username: "peter", Password: customerPassword, Name: "Peter Mwangi", Account: "1007730",
				Balance: 268000, KnownDevices: []string{"peter-phone"}, OpenedDaysAgo: 1650},
		},
	},
	{
		ID: "kcb", Name: "KCB", Port: env("KCB_PORT", "9102"), Accent: "#00843d",
		KifaruCode: "bank_b", KifaruEmail: "anthonyjordan@kcb.co.ke",
		BlockThreshold: 0.75, ReportThreshold: 0.45,
		Customers: []Customer{
			{Username: "faith", Password: customerPassword, Name: "Faith Njeri", Account: "1105108",
				Balance: 530000, KnownDevices: []string{"faith-phone"}, OpenedDaysAgo: 3400},
		},
	},
	{
		ID: "equity", Name: "Equity Bank", Port: env("EQUITY_PORT", "9103"), Accent: "#c41230",
		KifaruCode: "psp_c", KifaruEmail: "anthonyjordan@equitybank.co.ke",
		BlockThreshold: 0.80, ReportThreshold: 0.70,
		Customers: []Customer{
			{Username: "kiprono", Password: customerPassword, Name: "J. Kiprono", Account: "0114417",
				Balance: 0, KnownDevices: []string{"kiprono-phone"}, OpenedDaysAgo: 3},
		},
	},
}

const networkPort = "9100"

func bankByID(id string) (BankConfig, bool) {
	for _, bank := range banks {
		if bank.ID == id {
			return bank, true
		}
	}
	return BankConfig{}, false
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return strings.TrimSuffix(value, "/")
	}
	return fallback
}

// sessionIdle is how long a customer stays signed in without a sign-in,
// transfer or withdrawal. Set SESSION_IDLE_MINUTES to change it.
func sessionIdle() time.Duration {
	minutes, err := strconv.Atoi(env("SESSION_IDLE_MINUTES", "15"))
	if err != nil || minutes <= 0 {
		minutes = 15
	}
	return time.Duration(minutes) * time.Minute
}

func kifaruAPI() string      { return env("KIFARU_API_URL", "http://127.0.0.1:8000") }
func kifaruWeb() string      { return env("KIFARU_WEB_URL", "http://127.0.0.1:5173") }
func kifaruPassword() string { return env("KIFARU_DEMO_PASSWORD", "KifaruDemo2026!") }
func networkURL() string     { return env("NETWORK_URL", "http://127.0.0.1:"+env("NETWORK_PORT", networkPort)) }

// hashKey matches the Kifaru frontend's demo upload key, so identifiers hash
// the same way wherever they are protected.
func hashKey() string { return env("DEMO_HASH_KEY", "kifaru-demo-upload-key") }

func bankURL(id string) string {
	bank, _ := bankByID(id)
	return env(strings.ToUpper(id)+"_URL", "http://127.0.0.1:"+bank.Port)
}
