// Command origoad runs the Origoa Foundation backend: Git repository
// management, PostgreSQL projection, repository services, REST API and
// the WebSocket session service.
package main

import (
	"context"
	"flag"
	"log"
	"os/signal"
	"syscall"

	"origoa/internal/api"
	"origoa/internal/config"
	"origoa/internal/gitstore"
	"origoa/internal/projection"
	"origoa/internal/repo"
	"origoa/internal/scanner"
)

func main() {
	cfgPath := flag.String("config", "", "path to origoa.json")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}

	git, err := gitstore.Open(cfg.GitDir)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("origoa: git repository at %s", cfg.GitDir)

	db, err := projection.Connect(ctx, cfg.Database)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	sc, err := scanner.New(cfg.Scanner, git, db, &scanner.FoundationIndexer{DB: db})
	if err != nil {
		log.Fatal(err)
	}

	svc := repo.New(git, db, sc)
	svc.AuthorName = cfg.Author.Name
	svc.AuthorEmail = cfg.Author.Email

	// Recovery: replay commits missed while the backend was down.
	if err := svc.Sync(ctx); err != nil {
		log.Fatalf("origoa: startup synchronization failed: %v", err)
	}
	log.Printf("origoa: projection synchronized")

	server := api.NewServer(svc, cfg.StaticDir)
	if err := server.Serve(ctx, cfg.Listen); err != nil {
		log.Fatal(err)
	}
}
