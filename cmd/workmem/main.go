package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"workmem/internal/dotenv"
	"workmem/internal/mcpserver"
	"workmem/internal/store"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] == "serve" || os.Args[1][0] == '-' {
		runMCP(os.Args[1:])
		return
	}

	switch os.Args[1] {
	case "sqlite-canary":
		runSQLiteCanary(os.Args[2:])
	case "serve":
		runMCP(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		printUsage()
		os.Exit(2)
	}
}

func runMCP(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dbPath := fs.String("db", "", "path to the SQLite database file")
	envFile := fs.String("env-file", "", "path to a .env file to load before starting (process env wins over file values)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "parse flags: %v\n", err)
		os.Exit(2)
	}

	loadEnvFile(*envFile)

	runtime, err := mcpserver.New(mcpserver.Config{DBPath: *dbPath})
	if err != nil {
		fmt.Fprintf(os.Stderr, "start mcp server: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runtime.RunStdio(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "mcp server failed: %v\n", err)
		os.Exit(1)
	}
}

func runSQLiteCanary(args []string) {
	fs := flag.NewFlagSet("sqlite-canary", flag.ExitOnError)
	dbPath := fs.String("db", "", "path to the SQLite database file")
	envFile := fs.String("env-file", "", "path to a .env file to load before starting (process env wins over file values)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "parse flags: %v\n", err)
		os.Exit(2)
	}

	loadEnvFile(*envFile)

	result, err := store.RunSQLiteCanary(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sqlite canary failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("sqlite canary ok\n")
	fmt.Printf("driver=%s\n", result.Driver)
	fmt.Printf("database=%s\n", result.DatabasePath)
	fmt.Printf("observation_id=%d\n", result.ObservationID)
	fmt.Printf("fts_before_delete=%d\n", result.MatchCountBeforeDelete)
	fmt.Printf("fts_after_delete=%d\n", result.MatchCountAfterDelete)
	fmt.Printf("foreign_keys_enabled=%t\n", result.ForeignKeysEnabled)
	fmt.Printf("orphan_insert_rejected=%t\n", result.OrphanInsertRejected)
	fmt.Printf("persisted_observations=%d\n", result.PersistedObservationCount)
}

// loadEnvFile loads the given .env file if non-empty. Errors are logged to
// stderr and the process continues with whatever environment it already has —
// a failed .env load must not prevent the server from starting.
func loadEnvFile(path string) {
	if path == "" {
		return
	}
	if err := dotenv.Load(path); err != nil {
		fmt.Fprintf(os.Stderr, "[workmem] warning: cannot load env-file: %v\n", err)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "usage: workmem [serve] [flags]\n")
	fmt.Fprintf(os.Stderr, "       workmem <command> [flags]\n\n")
	fmt.Fprintf(os.Stderr, "commands:\n")
	fmt.Fprintf(os.Stderr, "  serve           run the MCP server over stdio (default)\n")
	fmt.Fprintf(os.Stderr, "  sqlite-canary   prove schema init, FTS insert/match/delete, and persistence\n\n")
	fmt.Fprintf(os.Stderr, "flags (all commands):\n")
	fmt.Fprintf(os.Stderr, "  -db <path>        path to the SQLite database file\n")
	fmt.Fprintf(os.Stderr, "  -env-file <path>  load variables from a .env file (process env takes precedence)\n")
}
