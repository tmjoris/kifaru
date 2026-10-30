package main

import (
	"crypto/subtle"
	"log"
	"net/http"
	"strings"
)

// hostedPath is where the demo lives when every service shares one port: the
// director at /demo-banks/ and each bank at /demo-banks/<bank>/.
const hostedPath = "/demo-banks"

// internalHeader carries a key the services send each other when they share a
// public port, so a visitor cannot call the switch, credit an account or reset
// a bank directly. Both values stay empty when each service has its own
// loopback port.
const internalHeader = "X-Demo-Internal-Key"

var (
	internalKey  string
	publicPrefix string
)

// runHosted serves the director, the switch and all three banks from one
// port, which is what a host such as Render gives a web service. The services
// still reach each other over HTTP, through that same port.
func runHosted() {
	port := env("PORT", networkPort)
	internal := "http://127.0.0.1:" + port + hostedPath
	setDefault("NETWORK_URL", internal)
	for _, cfg := range banks {
		setDefault(strings.ToUpper(cfg.ID)+"_URL", internal+"/"+cfg.ID)
	}
	internalKey = randomID(16)
	publicPrefix = hostedPath

	services := map[string]http.Handler{}
	for _, cfg := range banks {
		bank := newBank(cfg)
		if cfg.ID == "equity" {
			go bank.pollAlerts()
		}
		services[cfg.ID] = bank.routes()
	}
	log.Printf("Kifaru API: %s", kifaruAPI())
	serve("Director, switch and banks", "0.0.0.0:"+port, hostedRoutes(newNetwork().routes(), services, internalKey))
}

// hostedRoutes mounts the network and each bank under hostedPath.
func hostedRoutes(network http.Handler, services map[string]http.Handler, key string) http.Handler {
	mux := http.NewServeMux()
	mount := func(prefix string, handler http.Handler) {
		mux.Handle(prefix+"/", http.StripPrefix(prefix, internalOnly(key, handler)))
		mux.Handle(prefix, http.RedirectHandler(prefix+"/", http.StatusMovedPermanently))
	}
	mount(hostedPath, network)
	for id, handler := range services {
		mount(hostedPath+"/"+id, handler)
	}
	mux.Handle("GET /{$}", http.RedirectHandler(hostedPath+"/", http.StatusFound))
	return mux
}

// internalOnly refuses the service-to-service routes unless the request
// carries the key.
func internalOnly(key string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if internalRoute(r) && subtle.ConstantTimeCompare([]byte(r.Header.Get(internalHeader)), []byte(key)) != 1 {
			writeError(w, http.StatusForbidden, "This route is only for the demo services.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func internalRoute(r *http.Request) bool {
	path := r.URL.Path
	return strings.HasPrefix(path, "/switch/") || strings.HasPrefix(path, "/interbank/") ||
		strings.HasPrefix(path, "/admin/") || (path == "/mail" && r.Method == http.MethodPost)
}

// demoService reports whether url points at the switch or one of the banks,
// the only places the internal key is sent.
func demoService(url string) bool {
	if strings.HasPrefix(url, networkURL()+"/") {
		return true
	}
	for _, bank := range banks {
		if strings.HasPrefix(url, bankURL(bank.ID)+"/") {
			return true
		}
	}
	return false
}

// publicBankURL is the bank's address as the browser sees it.
func publicBankURL(id string) string {
	if publicPrefix != "" {
		return publicPrefix + "/" + id
	}
	return bankURL(id)
}
