package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required and must point to PostgreSQL")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, databaseURL)
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
		streams: map[string]map[chan []byte]struct{}{},
	}
	if err := app.initDB(ctx); err != nil {
		log.Fatal(err)
	}
	go app.runDemoProducer(context.Background())

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
		MaxHeaderBytes:    1 << 20,
	}
	log.Printf("KIFARU Go API listening on %s", server.Addr)
	log.Fatal(server.ListenAndServe())
}
