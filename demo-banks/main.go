// Command demo-banks runs three simulated banks and the network between them
// for the Kifaru attack demo. Each bank keeps its own customers, ledger and
// fraud rules, and reaches Kifaru only through its public API.
//
//	go run .              run the network and all three banks
//	go run . network      run only the switch, mailbox and director page
//	go run . bank ncba    run one bank (ncba, kcb or equity)
package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"
)

//go:embed web
var webFiles embed.FS

func page(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		content, err := webFiles.ReadFile("web/" + name)
		if err != nil {
			http.NotFound(w, nil)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(content)
	}
}

func staticFiles() http.Handler {
	sub, _ := fs.Sub(webFiles, "web")
	return http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
}

// listenAddr keeps every service on the loopback interface. The ops, reset
// and interbank routes have no sign-in, which is only safe while nothing
// outside this machine can reach them.
func listenAddr(port string) string { return "127.0.0.1:" + port }

func serve(name, port string, handler http.Handler) {
	log.Printf("%s listening on http://%s", name, listenAddr(port))
	server := &http.Server{Addr: listenAddr(port), Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(server.ListenAndServe())
}

func runBank(id string) {
	cfg, ok := bankByID(id)
	if !ok {
		log.Fatalf("unknown bank %q (use ncba, kcb or equity)", id)
	}
	bank := newBank(cfg)
	if cfg.ID == "equity" {
		go bank.pollAlerts()
	}
	serve(cfg.Name, cfg.Port, bank.routes())
}

func main() {
	args := os.Args[1:]
	switch {
	case len(args) == 0:
		for _, cfg := range banks {
			go runBank(cfg.ID)
		}
		log.Printf("Kifaru API: %s", kifaruAPI())
		serve("Director and interbank switch", env("NETWORK_PORT", networkPort), newNetwork().routes())
	case args[0] == "network":
		serve("Director and interbank switch", env("NETWORK_PORT", networkPort), newNetwork().routes())
	case args[0] == "bank" && len(args) == 2:
		runBank(args[1])
	default:
		log.Fatal("usage: demo-banks [network | bank ncba|kcb|equity]")
	}
}
