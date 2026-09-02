package harnesses

import "time"

// Discovery is what both session-id discoverers are told about the session
// they are looking for.
//
// The three fields are three different ways of not claiming somebody else's
// conversation. Cwd is the weakest: two sessions of one harness can share a
// directory, and neither CLI offers a per-session handle to tell them apart.
// Since rules out everything that existed before this pane started, which is
// what makes a preset or a typed-in directory with history safe. Claimed rules
// out the ids other rows have already taken, which is what keeps a second
// session in the same directory from adopting the first one's conversation.
type Discovery struct {
	// Cwd is the absolute working directory the program was launched in.
	Cwd string
	// Since is when the program was launched. Anything the CLI wrote before
	// that belongs to somebody else.
	Since time.Time
	// Claimed reports whether an id is already recorded against another
	// session of this harness. It may be nil, which means nothing is claimed.
	Claimed func(id string) bool
}

// claimed is Claimed with the nil case folded in.
func (d Discovery) claimed(id string) bool {
	return d.Claimed != nil && d.Claimed(id)
}

// startedBefore reports whether something the CLI wrote at t belongs to an
// earlier session than this one.
//
// The allowance is for the two clocks involved: the id or the file is stamped
// by the CLI process a moment after Socrates noted the launch time, but the
// stamp's resolution is coarse (OpenCode's ids carry milliseconds, a file's
// mtime may carry seconds), so an exact comparison would drop the very session
// it is meant to find.
func (d Discovery) startedBefore(t time.Time) bool {
	return t.Before(d.Since.Add(-discoverySkew))
}

// discoverySkew is how much older than the launch a stamp may be and still
// count as this session's.
const discoverySkew = 5 * time.Second
