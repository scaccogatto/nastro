// Command nastro is a macOS terminal call-recorder CLI/TUI.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/scaccogatto/nastro/internal/config"
	"github.com/scaccogatto/nastro/internal/record"
	"github.com/scaccogatto/nastro/internal/records"
	"github.com/scaccogatto/nastro/internal/transcribe"
	"github.com/scaccogatto/nastro/internal/tui"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: load config: %v\n", err)
		os.Exit(1)
	}

	args := os.Args[1:]
	if len(args) == 0 {
		runOrExit(func() error { return tui.Run(cfg, scanOrExit(cfg)) })
		return
	}

	switch args[0] {
	case "record":
		runOrExit(func() error { return runRecord(cfg, args[1:]) })
	case "records":
		runOrExit(func() error { return runRecords(cfg, args[1:]) })
	case "transcribe":
		runOrExit(func() error { return runTranscribe(cfg, args[1:]) })
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\nusage: nastro [record|records|transcribe] ...\n", args[0])
		os.Exit(1)
	}
}

func runRecord(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("record", flag.ExitOnError)
	name := fs.String("name", "", "slug for the recording")
	micOnly := fs.Bool("mic-only", false, "record microphone only")
	systemOnly := fs.Bool("system-only", false, "record system audio only")
	fs.Parse(args)

	return record.Run(cfg, record.Options{Name: *name, MicOnly: *micOnly, SystemOnly: *systemOnly})
}

func runRecords(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("records", flag.ExitOnError)
	plain := fs.Bool("plain", false, "tab-separated output, one record per line")
	fs.Parse(args)

	recs, err := records.Scan(cfg.OutputDir)
	if err != nil {
		return fmt.Errorf("scan records: %w", err)
	}

	if *plain {
		for _, r := range recs {
			fmt.Println(records.PlainLine(r))
		}
		return nil
	}

	return tui.Run(cfg, recs)
}

func runTranscribe(cfg config.Config, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: nastro transcribe <id|last>")
	}
	return transcribe.Run(cfg, args[0])
}

func scanOrExit(cfg config.Config) []records.Record {
	recs, err := records.Scan(cfg.OutputDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: scan records: %v\n", err)
		os.Exit(1)
	}
	return recs
}

func runOrExit(fn func() error) {
	if err := fn(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
