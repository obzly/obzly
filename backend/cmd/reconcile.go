package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"obzly/internal/config"
	"obzly/internal/storage/segments"

	"github.com/google/uuid"
)

// runReconcile is `obzly reconcile`: the catalog against object storage,
// after a database restore or an object-storage incident (docs/runbooks).
// Reports by default; --apply repairs what it safely can. Exit code 2 when
// findings remain for a person.
func runReconcile(args []string) int {
	flags := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	apply := flags.Bool("apply", false, "repair: re-upload, mark lost, adopt orphans (default: report only)")
	streams := flags.String("streams", "", "comma-separated streams (default: all)")
	projects := flags.String("projects", "", "comma-separated project ids (default: all)")
	orphanMinAge := flags.Duration("orphan-min-age", time.Hour,
		"skip objects newer than this (uploads and compaction deletes in flight)")
	adoptSince := flags.String("adopt-since", "",
		"the database restore point (RFC 3339): orphans uploaded since are adopted with --apply; "+
			"older ones never are (retired data). Without it orphans are only reported")
	asJSON := flags.Bool("json", false, "print the report as JSON")
	if err := flags.Parse(args); err != nil {
		return 1
	}

	env := config.GetEnv()
	if env.ObjectStorageURL == "" {
		fmt.Fprintln(os.Stderr, "reconcile: OBJECT_STORAGE_URL is not set - nothing to reconcile against")
		return 1
	}
	options := segments.ReconcileOptions{
		Apply: *apply, OrphanMinAge: *orphanMinAge, DataDir: env.LogsDataDir,
	}
	options.Streams = append(options.Streams, splitList(*streams)...)
	if *adoptSince != "" {
		since, err := time.Parse(time.RFC3339, *adoptSince)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reconcile: --adopt-since must be RFC 3339 (2026-10-06T11:00:00Z): %v\n", err)
			return 1
		}
		options.AdoptSince = since
	}
	for _, raw := range splitList(*projects) {
		id, err := uuid.Parse(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reconcile: bad project id %q\n", raw)
			return 1
		}
		options.Projects = append(options.Projects, id)
	}

	ctx := context.Background()
	store, err := segments.NewObjectStore(ctx, env.ObjectStorageURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reconcile:", err)
		return 1
	}
	defer func() { _ = store.Close() }()
	report, err := segments.Reconcile(ctx, store, options)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reconcile:", err)
		return 1
	}

	if *asJSON {
		encoded, _ := json.MarshalIndent(report, "", "  ")
		fmt.Println(string(encoded))
	} else {
		printReconcileReport(report)
	}
	for _, finding := range report.Findings {
		if !finding.Fixed {
			return 2
		}
	}
	return 0
}

func printReconcileReport(report *segments.ReconcileReport) {
	mode := "report only (--apply to repair)"
	if report.Applied {
		mode = "applied"
	}
	fmt.Printf("Checked %d cataloged segments and %d objects - %s\n\n", report.RowsChecked, report.ObjectsListed, mode)
	if len(report.Findings) == 0 {
		fmt.Println("Catalog and object storage agree.")
		return
	}
	writer := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "KIND\tSTREAM\tSEGMENT\tFIXED\tDETAIL")
	for _, finding := range report.Findings {
		id := finding.SegmentID
		if id == "" {
			id = finding.ObjectKey
		}
		_, _ = fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%t\t%s\n",
			finding.Kind,
			finding.Stream,
			id,
			finding.Fixed,
			finding.Detail,
		)
	}
	_ = writer.Flush()
}

func splitList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
