package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"workmem/internal/mcpserver"
	"workmem/internal/reconcile"
	"workmem/internal/store"
)

func runReconcile(args []string) {
	fs := flag.NewFlagSet("reconcile", flag.ExitOnError)
	dbPath := fs.String("db", "", "path to the SQLite database file for global scope")
	envFile := fs.String("env-file", "", "path to a .env file to load before running (process env wins over file values)")
	mode := fs.String("mode", "propose", "reconcile mode (v0 supports propose only)")
	scope := fs.String("scope", "global", "scan scope: global or project=<path>")
	sinceRaw := fs.String("since", "30d", "scan window duration, e.g. 30d or 720h")
	minObsPerEntity := fs.Int("min-obs-per-entity", 2, "minimum active observations per scanned entity")
	maxEntitiesPerRun := fs.Int("max-entities-per-run", 50, "maximum entities to scan")
	output := fs.String("output", "", "markdown report path (default: review/reconcile-<timestamp>.md)")
	_ = fs.Parse(args)

	loadEnvFile(*envFile)

	if *mode != "propose" {
		fmt.Fprintf(os.Stderr, "reconcile: unsupported --mode %q (v0 supports propose only)\n", *mode)
		os.Exit(2)
	}
	if *minObsPerEntity <= 0 {
		fmt.Fprintln(os.Stderr, "reconcile: --min-obs-per-entity must be > 0")
		os.Exit(2)
	}
	if *maxEntitiesPerRun <= 0 {
		fmt.Fprintln(os.Stderr, "reconcile: --max-entities-per-run must be > 0")
		os.Exit(2)
	}
	since, err := parseReconcileSince(*sinceRaw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: invalid --since: %v\n", err)
		os.Exit(2)
	}

	db, release, scopeLabel, err := openReconcileDB(*scope, *dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: %v\n", err)
		os.Exit(1)
	}
	defer release()

	report, err := store.BuildReconcileProposeReport(db, store.ReconcileProposeOptions{
		GeneratedAt:       time.Now().UTC(),
		Since:             since,
		SinceLabel:        *sinceRaw,
		MinObsPerEntity:   *minObsPerEntity,
		MaxEntitiesPerRun: *maxEntitiesPerRun,
		Scope:             scopeLabel,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: build propose report: %v\n", err)
		os.Exit(1)
	}
	reportPath, err := reconcile.WriteProposeReport(*output, report)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("reconcile propose: wrote %s (%d exact duplicate group(s), %d candidate(s), 0 mutation(s))\n",
		reportPath,
		len(report.DuplicateGroups),
		report.CandidatesProposed,
	)
}

func openReconcileDB(scopeValue string, dbPath string) (*sql.DB, func(), string, error) {
	scopeValue = strings.TrimSpace(scopeValue)
	if scopeValue == "" || scopeValue == "global" {
		resolved, err := mcpserver.ResolveDBPath(dbPath)
		if err != nil {
			return nil, nil, "", fmt.Errorf("resolve global db: %w", err)
		}
		db, err := store.OpenReadOnlyDB(resolved)
		if err != nil {
			return nil, nil, "", fmt.Errorf("open global db read-only: %w", err)
		}
		return db, func() { _ = db.Close() }, "global", nil
	}
	const projectPrefix = "project="
	if !strings.HasPrefix(scopeValue, projectPrefix) {
		return nil, nil, "", fmt.Errorf("invalid --scope %q (use global or project=<path>)", scopeValue)
	}
	project := strings.TrimSpace(strings.TrimPrefix(scopeValue, projectPrefix))
	if project == "" {
		return nil, nil, "", fmt.Errorf("invalid --scope %q: project path is empty", scopeValue)
	}
	if strings.TrimSpace(dbPath) != "" {
		return nil, nil, "", fmt.Errorf("--db is only valid with --scope global")
	}
	resolved, projectDBPath := store.ResolveProjectDBPath(project, "")
	db, err := store.OpenReadOnlyDB(projectDBPath)
	if err != nil {
		return nil, nil, "", fmt.Errorf("open project db read-only: %w", err)
	}
	return db, func() { _ = db.Close() }, "project:" + resolved, nil
}

func parseReconcileSince(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if strings.HasSuffix(value, "d") {
		daysText := strings.TrimSuffix(value, "d")
		days, err := time.ParseDuration(daysText + "h")
		if err != nil {
			return 0, fmt.Errorf("parse day count %q: %w", daysText, err)
		}
		duration := days * 24
		if duration <= 0 {
			return 0, fmt.Errorf("duration must be > 0")
		}
		return duration, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, err
	}
	if duration <= 0 {
		return 0, fmt.Errorf("duration must be > 0")
	}
	return duration, nil
}
