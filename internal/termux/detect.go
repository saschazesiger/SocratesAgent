package termux

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// This file is the answer to the one question the dashboard asks about the
// machine underneath it: is tmux here, and is it new enough.
//
// Nothing in it is a fallback. tmux missing is a hard, explained error - the
// new-session sheet refuses to start anything - so the whole job here is to
// say what is wrong in words somebody can act on.
//
// There is deliberately nothing here that installs it. Socrates is run as the
// Docker image, and the image has tmux; a machine without it is a broken
// image, not a machine waiting for a package manager.

// Report is what the dashboard knows about tmux on this machine.
type Report struct {
	Installed bool   `json:"installed"`
	Path      string `json:"path"`
	Version   string `json:"version"`
	// OK is the only field that decides anything: it is true when tmux is
	// here *and* new enough for the generated configuration.
	OK  bool   `json:"ok"`
	Min string `json:"min"`
	// Reason says in one sentence why OK is false.
	Reason string `json:"reason"`
}

// Detector answers that question. LookPath is the machine, injected so a test
// can describe one without being run on it; nil means the real thing.
type Detector struct {
	LookPath func(file string) (string, error)
}

func (d Detector) lookPath(file string) (string, error) {
	if d.LookPath != nil {
		return d.LookPath(file)
	}
	return exec.LookPath(file)
}

// Detect answers the /api/tmux question: the binary, then its version.
func (d Detector) Detect(ctx context.Context) Report {
	rep := Report{Min: fmt.Sprintf("%d.%d", MinMajor, MinMinor)}
	path, err := d.lookPath("tmux")
	if err != nil {
		rep.Reason = "tmux is not installed. Socrates needs it to keep sessions alive, " +
			"and the Docker image ships with it - this one is missing it"
		return rep
	}
	rep.Installed, rep.Path = true, path
	probe, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	v, verr := BinaryVersion(probe, path)
	switch {
	case verr != nil:
		rep.Reason = fmt.Sprintf("could not run %s: %v", path, verr)
	case !v.OK():
		rep.Version = v.String()
		rep.Reason = fmt.Sprintf("tmux %s is too old; Socrates needs %s or newer", v, rep.Min)
	default:
		rep.Version, rep.OK = v.String(), true
	}
	return rep
}
