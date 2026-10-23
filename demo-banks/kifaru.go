package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// protect turns a raw identifier into the keyed hash Kifaru accepts. The
// run salt changes on every demo reset so signals from an earlier run never
// match a later one.
func protect(salt, value string) string {
	mac := hmac.New(sha256.New, []byte(hashKey()))
	normalized := strings.ToLower(strings.Join(strings.Fields(value), ""))
	if salt != "" {
		normalized = salt + ":" + normalized
	}
	mac.Write([]byte(normalized))
	return "sha256:" + hex.EncodeToString(mac.Sum(nil))
}

// KifaruClient is a bank's connector to Kifaru. It signs in with the bank's
// own institution account and uses only the public API.
type KifaruClient struct {
	email       string
	institution string
	http        *http.Client

	mu    sync.Mutex
	token string
	csrf  string
}

func newKifaruClient(email, institution string) *KifaruClient {
	return &KifaruClient{email: email, institution: institution, http: &http.Client{Timeout: 20 * time.Second}}
}

type kifaruError struct {
	status  int
	message string
}

func (e *kifaruError) Error() string { return fmt.Sprintf("Kifaru %d: %s", e.status, e.message) }

func (k *KifaruClient) login() error {
	body, _ := json.Marshal(map[string]any{
		"email": k.email, "password": kifaruPassword(),
		"institution_code": k.institution, "keep_signed_in": true,
	})
	response, err := k.http.Post(kifaruAPI()+"/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("Kifaru is unreachable at %s: %w", kifaruAPI(), err)
	}
	defer response.Body.Close()
	var payload struct {
		AccessToken string `json:"access_token"`
		CSRFToken   string `json:"csrf_token"`
		Error       string `json:"error"`
		Detail      string `json:"detail"`
	}
	_ = json.NewDecoder(response.Body).Decode(&payload)
	if response.StatusCode != http.StatusOK || payload.AccessToken == "" {
		return &kifaruError{response.StatusCode, "sign-in failed for " + k.email + ": " + payload.Error + payload.Detail}
	}
	k.token, k.csrf = payload.AccessToken, payload.CSRFToken
	return nil
}

// call sends one API request, signing in first and once more if the session
// has expired.
func (k *KifaruClient) call(method, path string, in, out any) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		if k.token == "" {
			if err := k.login(); err != nil {
				return err
			}
		}
		var reader io.Reader
		if in != nil {
			encoded, _ := json.Marshal(in)
			reader = bytes.NewReader(encoded)
		}
		request, err := http.NewRequest(method, kifaruAPI()+path, reader)
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+k.token)
		request.Header.Set("X-Kifaru-CSRF", k.csrf)
		if in != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := k.http.Do(request)
		if err != nil {
			return fmt.Errorf("Kifaru is unreachable at %s: %w", kifaruAPI(), err)
		}
		raw, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode == http.StatusUnauthorized && attempt == 0 {
			k.token = ""
			continue
		}
		if response.StatusCode >= 300 {
			var problem struct {
				Error  string `json:"error"`
				Detail string `json:"detail"`
			}
			_ = json.Unmarshal(raw, &problem)
			return &kifaruError{response.StatusCode, problem.Error + problem.Detail}
		}
		if out != nil {
			return json.Unmarshal(raw, out)
		}
		return nil
	}
	return errors.New("Kifaru session could not be renewed")
}

// KifaruResult is what Kifaru returned for a published report.
type KifaruResult struct {
	ReportID string `json:"report_id"`
	Status   string `json:"status"`
	Score    float64
	AlertID  string `json:"alert_id"`
	Tier     string `json:"tier"`
}

func (k *KifaruClient) publish(report map[string]any) (KifaruResult, error) {
	var payload struct {
		ReportID   string `json:"report_id"`
		Validation struct {
			Status          string  `json:"status"`
			ValidationScore float64 `json:"validation_score"`
		} `json:"validation"`
		Alert map[string]any `json:"alert"`
	}
	if err := k.call(http.MethodPost, "/v1/reports", report, &payload); err != nil {
		return KifaruResult{}, err
	}
	result := KifaruResult{ReportID: payload.ReportID, Status: payload.Validation.Status,
		Score: payload.Validation.ValidationScore}
	if payload.Alert != nil {
		result.AlertID = fmt.Sprint(payload.Alert["alert_id"])
		result.Tier = fmt.Sprint(payload.Alert["alert_tier"])
	}
	return result, nil
}

// RiskSignal is Kifaru's answer for one checked identifier. It says only
// whether other institutions reported it, how strongly and how many.
type RiskSignal struct {
	Checked      bool   `json:"checked"`
	Flagged      bool   `json:"flagged"`
	Tier         string `json:"tier"`
	Institutions int    `json:"institutions"`
}

// RiskCheck is Kifaru's pre-payment or sign-in answer.
type RiskCheck struct {
	Device      RiskSignal `json:"device"`
	Destination RiskSignal `json:"destination"`
}

// check asks Kifaru about a protected device and, when given, a protected
// destination account at destinationInstitution.
func (k *KifaruClient) check(device, destinationAccount, destinationInstitution string) (RiskCheck, error) {
	body := map[string]string{"device_profile": device}
	if destinationAccount != "" {
		body["destination_account_hash"] = destinationAccount
		body["destination_institution"] = destinationInstitution
	}
	var result RiskCheck
	err := k.call(http.MethodPost, "/v1/risk-check?institution="+k.institution, body, &result)
	return result, err
}

// KifaruAlert is an alert routed to a receiving bank.
type KifaruAlert struct {
	AlertID                string  `json:"alert_id"`
	ReportID               string  `json:"report_id"`
	IssuedAt               string  `json:"issued_at"`
	ReportingInstitution   string  `json:"reporting_institution"`
	DestinationAccountHash string  `json:"destination_account_hash"`
	Amount                 float64 `json:"amount"`
	Score                  float64 `json:"validation_score"`
	Explanation            string  `json:"explanation"`
	State                  string  `json:"state"`
	Outcome                string  `json:"outcome"`
	Tier                   string  `json:"alert_tier"`
}

func (k *KifaruClient) alerts() ([]KifaruAlert, error) {
	var payload struct {
		Alerts []KifaruAlert `json:"alerts"`
	}
	err := k.call(http.MethodGet, "/v1/alerts?institution="+k.institution+"&limit=50", nil, &payload)
	return payload.Alerts, err
}

func (k *KifaruClient) setAlertState(alertID, state, outcome, comment string) error {
	return k.call(http.MethodPost, "/v1/alerts/"+alertID+"/state",
		map[string]string{"state": state, "outcome": outcome, "comment": comment}, nil)
}

// decide moves an alert to the receiver's outcome, acknowledging it first
// and passing through a hold when the outcome needs one.
func (k *KifaruClient) decide(alert KifaruAlert, outcome, comment string) error {
	state, current := alert.State, alert.Outcome
	if state == "sent" {
		if err := k.setAlertState(alert.AlertID, "acknowledged", "", comment); err != nil {
			return err
		}
		state = "acknowledged"
	}
	if outcome == "recovered" && current != "held" {
		if err := k.setAlertState(alert.AlertID, "actioned", "held", comment); err != nil {
			return err
		}
	}
	if state == "actioned" && current == outcome {
		return nil
	}
	return k.setAlertState(alert.AlertID, "actioned", outcome, comment)
}

func (k *KifaruClient) retract(reportID, comment string) error {
	return k.call(http.MethodPatch, "/v1/reports/"+reportID+"/lifecycle",
		map[string]string{"state": "retracted", "comment": comment}, nil)
}
