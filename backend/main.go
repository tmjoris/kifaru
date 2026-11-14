package main

import (
	"context"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required and must point to PostgreSQL")
	}
	tuneRuntime()
	startProfiler()
	ctx := context.Background()
	db, err := openPool(ctx, databaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err := db.Ping(ctx); err != nil {
		log.Fatalf("connect to PostgreSQL: %v", err)
	}

	raw, standard, err := loadStandard()
	if err != nil {
		log.Fatal(err)
	}
	app := &App{
		db: db, standardRaw: raw, standard: standard,
	}
	if err := app.initDB(ctx); err != nil {
		log.Fatal(err)
	}
	go app.runDemoProducer(context.Background())
	go app.runSessionFlusher(context.Background())
	go app.runHubFlusher(context.Background())
	go app.runRetention(context.Background())

	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           app.withCORS(http.HandlerFunc(app.serveHTTP)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       20 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	log.Printf("KIFARU Go API listening on %s", server.Addr)
	log.Fatal(server.ListenAndServe())
}

// openPool builds the connection pool. The defaults give four connections,
// which is too few once the database is a network hop away, so the size is
// explicit and can be changed with DB_MAX_CONNS and DB_MIN_CONNS.
//
// DB_QUERY_MODE=exec turns off server-side prepared statements, which is what
// a transaction-mode pooler such as Neon's "-pooler" endpoint needs if it is
// not configured for protocol-level prepared statements. It also makes every
// query a single round trip.
func openPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = int32(envInt("DB_MAX_CONNS", 60))
	cfg.MinConns = int32(envInt("DB_MIN_CONNS", 2))
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnLifetimeJitter = 5 * time.Minute
	cfg.MaxConnIdleTime = 4 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	cfg.ConnConfig.ConnectTimeout = 15 * time.Second
	switch os.Getenv("DB_QUERY_MODE") {
	case "exec":
		cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	case "simple":
		cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}

func envString(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	if value, err := strconv.Atoi(os.Getenv(name)); err == nil && value > 0 {
		return value
	}
	return fallback
}

// startProfiler serves Go's pprof endpoints, but only when PPROF_ADDR is set
// (for example 127.0.0.1:6060), and never on the public port. It is how the
// CPU cost of a request is measured under load.
func startProfiler() {
	addr := os.Getenv("PPROF_ADDR")
	if addr == "" {
		return
	}
	go func() {
		log.Printf("pprof listening on %s", addr)
		log.Print(http.ListenAndServe(addr, nil)) // net/http/pprof registers on the default mux
	}()
}
