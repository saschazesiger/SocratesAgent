package term

import "testing"

func TestKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"enter", "\r"},
		{"Enter", "\r"},
		{" escape ", "\x1b"},
		{"up", "\x1b[A"},
		{"down", "\x1b[B"},
		{"shift+tab", "\x1b[Z"},
		{"ctrl+c", "\x03"},
		{"ctrl+D", "\x04"},
		{"alt+b", "\x1bb"},
		{"f5", "\x1b[15~"},
		{"1", "1"},
		{"y", "y"},
		{"ä", "ä"},
	}
	for _, c := range cases {
		got, err := Key(c.in)
		if err != nil {
			t.Errorf("Key(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Key(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestKeyRejectsNonsense(t *testing.T) {
	for _, in := range []string{"", "  ", "ctrl+shift+q", "banana", "alt+word"} {
		if _, err := Key(in); err == nil {
			t.Errorf("Key(%q) should have failed", in)
		}
	}
}

func TestKeys(t *testing.T) {
	got, err := Keys([]string{"y", "enter", "ctrl+c"})
	if err != nil {
		t.Fatalf("Keys: %v", err)
	}
	if want := "y\r\x03"; got != want {
		t.Errorf("Keys = %q, want %q", got, want)
	}
	if _, err := Keys([]string{"enter", "nope"}); err == nil {
		t.Error("an unknown key in the middle should fail the whole sequence")
	}
}
