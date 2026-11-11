package agenthost

import (
	"strings"
	"testing"
)

// A chat's git configuration takes the person's own in, and puts the helper
// first for the host of the repository's clone URL; a file:// remote needs
// none.
func TestRepoGitConfig(t *testing.T) {
	got := RepoGitConfig([]string{"HOME=/home/me"}, "https://github.com/octo/app.git", "/usr/bin/so crates", "/var/lib/socrates", "c1")
	for _, want := range []string{
		"[include]\n\tpath = \"/home/me/.config/git/config\"\n\tpath = \"/home/me/.gitconfig\"\n",
		"[credential \"https://github.com\"]\n\thelper =\n",
		"\thelper = \"!'/usr/bin/so crates' git-credential --data '/var/lib/socrates' --chat 'c1'\"\n",
		"\tuseHttpPath = true\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the configuration lacks %q:\n%s", want, got)
		}
	}
	own := RepoGitConfig([]string{"HOME=/home/me", "GIT_CONFIG_GLOBAL=/etc/me.gitconfig"}, "file:///srv/octo/app.git", "/s", "/d", "c1")
	if !strings.Contains(own, `path = "/etc/me.gitconfig"`) || strings.Contains(own, ".gitconfig\"\n\tpath") || strings.Contains(own, "[credential") {
		t.Fatalf("with a global configuration of its own and a file:// remote:\n%s", own)
	}
	req := readCredentialRequest(strings.NewReader("protocol=https\nhost=github.com\npath=octo/app.git\n\nignored=1\n"))
	if req != (CredentialRequest{Protocol: "https", Host: "github.com", Path: "octo/app.git"}) {
		t.Fatalf("the request: %+v", req)
	}
}
