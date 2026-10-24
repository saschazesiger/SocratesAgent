package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/saschazesiger/SocratesAgent/internal/backup"
	"github.com/saschazesiger/SocratesAgent/internal/config"
)

// restoreCommand is `socrates restore`: the setup page's restore, for a
// machine set up over SSH. It reads the bucket and the passphrase from the
// data directory's backup.json - copied there by hand from the old machine,
// or written by the setup page - and refuses a data directory that already
// holds an installation or that a running Socrates serves.
func restoreCommand(args []string) {
	os.Exit(restoreMain(args, os.Stdout, os.Stderr))
}

// restoreMain is restoreCommand with its output and its exit code in the
// caller's hands.
func restoreMain(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("socrates restore", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data", config.DataDir(), "the data directory to restore into")
	snapshot := fs.String("snapshot", "", "the snapshot to restore: its id (host/time, from -list) or latest")
	list := fs.Bool("list", false, "list the snapshots in the bucket and exit")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "Usage:\n  socrates restore -list [-data dir]\n  socrates restore -snapshot <id|latest> [-data dir]\n\n"+
			"Restores a backup into an empty data directory, with the bucket and the passphrase in its backup.json.\n"+
			"Stop Socrates first (systemctl stop socrates) and start it again afterwards.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 || (!*list && *snapshot == "") {
		fs.Usage()
		return 2
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "socrates restore: "+format+"\n", a...)
		return 1
	}
	cfg, err := backup.LoadConfig(*dataDir)
	if err != nil {
		return fail("%v", err)
	}
	if cfg == nil {
		return fail("%s has no backup.json: copy the old machine's there, or write one with the bucket and the passphrase", *dataDir)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *list {
		snaps, err := backup.List(ctx, *cfg, *dataDir)
		if err != nil {
			return fail("%s", backup.Explain(err))
		}
		if len(snaps) == 0 {
			fmt.Fprintln(stdout, "The bucket holds no snapshot.")
			return 0
		}
		for _, s := range snaps {
			what := fmt.Sprintf("%8s  %d files", size(s.Size), s.Files)
			if s.Empty {
				what = "holds no installation, not restorable"
			}
			fmt.Fprintf(stdout, "%-48s %s  %s\n", s.ID, time.UnixMilli(s.At).Local().Format("2006-01-02 15:04"), what)
		}
		return 0
	}

	unlock, err := lockDataDir(*dataDir)
	if err != nil {
		return fail("%v; stop it first (systemctl stop socrates)", err)
	}
	defer unlock()
	if holds, why, err := backup.HoldsStore(*dataDir); err != nil {
		return fail("%v", err)
	} else if holds {
		return fail("%v: %s", backup.ErrNotEmpty, why)
	}
	// The machine restored onto is a machine of its own in the bucket, from
	// before the first byte is restored: a restore that fails half way, or
	// a Socrates started before it is done, must never back up under the
	// old machine's id.
	cfg.HostID = ""
	if err := backup.SaveConfig(*dataDir, *cfg); err != nil {
		return fail("backup.json could not be written: %v", err)
	}
	last := time.Time{}
	summary, err := backup.Restore(ctx, backup.RestoreOptions{
		Config: *cfg, Snapshot: *snapshot, DataDir: *dataDir,
		Progress: func(p backup.Progress) {
			if time.Since(last) < 2*time.Second && p.FilesDone < p.Files {
				return
			}
			last = time.Now()
			fmt.Fprintf(stdout, "%s: %d of %d files, %s of %s\n", p.Snapshot, p.FilesDone, p.Files, size(p.BytesDone), size(p.Bytes))
		},
	})
	if err != nil {
		return fail("%s", backup.Explain(err))
	}
	if _, err := backup.ApplyPending(*dataDir); err != nil {
		return fail("the snapshot is staged but could not be moved into place (%v); starting Socrates tries again", err)
	}
	fmt.Fprintf(stdout, "Restored %s: %d files, %s. Start Socrates: systemctl start socrates\n", summary.ID, summary.Files, size(summary.Size))
	return 0
}

// size says a number of bytes the way a person reads it.
func size(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
