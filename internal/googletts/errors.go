package googletts

import "errors"

// ErrNoKey is the one failure that is a setup step rather than a fault: there
// is no credential, so nothing was even attempted. The handler turns it into
// the sentence that says where to add one.
var ErrNoKey = errors.New("no Google Cloud Text-to-Speech API key is configured")

// ErrNoText is a render of nothing, which is a caller's mistake and not the
// API's.
var ErrNoText = errors.New("there is no text to read out loud")

// errNoAudio is an answer that came back 200 and carried no audio. It has
// never been seen from the real API; it exists so a stand in that answers
// nonsense fails loudly instead of playing silence.
var errNoAudio = errors.New("Google Cloud Text-to-Speech returned no audio")
