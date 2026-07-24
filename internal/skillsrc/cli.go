package skillsrc

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
)

func RunCLI(ctx context.Context, args []string, options Options) int {
	if options.Out == nil {
		options.Out = io.Discard
	}
	if options.Err == nil {
		options.Err = io.Discard
	}
	global := flag.NewFlagSet("skillsrc", flag.ContinueOnError)
	global.SetOutput(options.Err)
	manifest := global.String("manifest", options.ManifestPath, "manifest path (default ~/.agents/.skillsrc)")
	lock := global.String("lock", "", "lockfile path (default beside manifest)")
	target := global.String("target", options.TargetDir, "installation target")
	cache := global.String("cache", options.CacheDir, "repository cache")
	git := global.String("git", options.GitBinary, "git executable")
	global.Usage = func() { printUsage(options.Err) }
	if err := global.Parse(args); err != nil {
		return 2
	}
	remaining := global.Args()
	if len(remaining) == 0 {
		printUsage(options.Err)
		return 2
	}
	manifestChanged := *manifest != global.Lookup("manifest").DefValue
	options.ManifestPath = *manifest
	if *lock != "" {
		options.LockPath = *lock
	} else if manifestChanged || options.LockPath == "" {
		options.LockPath = filepath.Join(filepath.Dir(*manifest), "skills.lock")
	}
	options.TargetDir, options.CacheDir, options.GitBinary = *target, *cache, *git
	engine := NewEngine(options)

	var err error
	switch remaining[0] {
	case "sync":
		if len(remaining) != 1 {
			return cliUsageError(options.Err, "sync accepts no arguments")
		}
		var result Result
		result, err = engine.Sync(ctx)
		if err == nil {
			fmt.Fprintf(options.Out, "synced (repository acquisitions: %d)\n", result.Acquisitions)
		}
	case "update":
		var result Result
		result, err = engine.Update(ctx, remaining[1:])
		if err == nil {
			for _, change := range result.Changes {
				old := change.Old
				if old == "" {
					old = "(unlocked)"
				}
				fmt.Fprintf(options.Out, "%s: %s -> %s\n", change.Source, old, change.New)
			}
			for _, local := range result.LocalSkipped {
				fmt.Fprintf(options.Out, "%s: local (no remote version)\n", local)
			}
			fmt.Fprintf(options.Out, "updated and synced (repository acquisitions: %d)\n", result.Acquisitions)
		}
	case "list":
		err = runListCLI(ctx, engine, remaining[1:], options.Out, options.Err)
	case "doctor":
		var issues bool
		issues, err = runDoctorCLI(ctx, engine, remaining[1:], options.Out, options.Err)
		if err == nil && issues {
			return 1
		}
	case "help":
		printUsage(options.Out)
		return 0
	default:
		return cliUsageError(options.Err, fmt.Sprintf("unknown command %q", remaining[0]))
	}
	if err != nil {
		fmt.Fprintf(options.Err, "skillsrc: %v\n", err)
		return 1
	}
	return 0
}

func runListCLI(ctx context.Context, engine *Engine, args []string, output, errorOutput io.Writer) error {
	flags := flag.NewFlagSet("list", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("list accepts no arguments")
	}
	statuses, err := engine.List(ctx)
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoder := json.NewEncoder(output)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(statuses)
	}
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "SKILL\tSOURCE\tREF\tCOMMIT\tSTATUS")
	for _, status := range statuses {
		ref, commit := status.ConfiguredRef, status.LockedCommit
		if ref == "" {
			ref = "(default/local)"
		}
		if commit == "" {
			commit = "-"
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", status.Name, status.Source, ref, commit, status.Status)
	}
	return writer.Flush()
}

func runDoctorCLI(ctx context.Context, engine *Engine, args []string, output, errorOutput io.Writer) (bool, error) {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	repair := flags.Bool("repair", false, "repair by running sync")
	jsonOutput := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return false, err
	}
	if flags.NArg() != 0 {
		return false, errors.New("doctor accepts no arguments")
	}
	report, err := engine.Doctor(ctx, *repair)
	if err != nil {
		return false, err
	}
	if *jsonOutput {
		encoder := json.NewEncoder(output)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(report); err != nil {
			return false, err
		}
	} else if len(report.Issues) == 0 {
		fmt.Fprintln(output, "ok")
	} else {
		for _, issue := range report.Issues {
			label := issue.Kind
			if issue.Skill != "" {
				label += "/" + issue.Skill
			}
			fmt.Fprintf(output, "%s: %s\n", label, issue.Message)
		}
	}
	return len(report.Issues) > 0, nil
}

func cliUsageError(output io.Writer, message string) int {
	fmt.Fprintf(output, "skillsrc: %s\n", message)
	printUsage(output)
	return 2
}

func printUsage(output io.Writer) {
	fmt.Fprintln(output, strings.TrimSpace(`skillsrc - Declarative skill dependencies for .agents/skills.

Usage:
  skillsrc [global flags] sync
  skillsrc [global flags] update [source-or-skill ...]
  skillsrc [global flags] list [--json]
  skillsrc [global flags] doctor [--repair] [--json]

Global flags:
  --manifest PATH  manifest (default ~/.agents/.skillsrc)
  --lock PATH      lockfile (default skills.lock beside manifest)
  --target PATH    installation directory (default ~/.agents/skills)
  --cache PATH     Git repository cache (default user cache/skillsrc/repos)`))
}
