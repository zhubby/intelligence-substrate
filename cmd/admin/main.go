package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	p "github.com/intelligence-substrate/platform/internal/platform"
)

func main() {
	data := flag.String("data", ".data", "Data directory")
	database := flag.String("database", os.Getenv("IS_DATABASE_URL"), "Database URL (default: SQLite in data directory)")
	archive := flag.String("archive", "", "Backup archive path")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: is-admin -data PATH [-database URL] [-archive FILE] backup|restore|verify")
		os.Exit(2)
	}
	root, e := filepath.Abs(*data)
	check(e)
	command := flag.Arg(0)
	if command == "restore" {
		check(p.Restore(context.Background(), *archive, root, *database))
		fmt.Println("Restore verified; sessions and API tokens were intentionally revoked.")
		return
	}
	if _, e = os.Stat(root); e != nil {
		check(e)
	}
	dsn := *database
	if dsn == "" {
		dsn = filepath.Join(root, "platform.db")
	}
	store, e := p.Open(dsn)
	check(e)
	defer store.Close()
	service := &p.Service{Store: store, Root: root}
	switch command {
	case "backup":
		check(service.Backup(context.Background(), *archive))
		fmt.Println("Backup created with private permissions.")
	case "verify":
		check(service.VerifyIntegrity(context.Background()))
		fmt.Println("Audit chain, artifacts and Git repositories verified.")
	default:
		fmt.Fprintln(os.Stderr, "unknown command")
		os.Exit(2)
	}
}
func check(e error) {
	if e != nil {
		slog.Error("operation failed", "error", e)
		os.Exit(1)
	}
}
