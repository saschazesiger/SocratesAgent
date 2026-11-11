package agenthost

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/saschazesiger/SocratesAgent/internal/agent/chattools"
)

// The credentials of a chat in a GitHub repository (docs/design/REPOS.md
// §5.2). The agent never holds a token: its git asks `socrates
// git-credential` for one whenever it reaches GitHub, which asks the server
// over the chat tools' socket, and the server answers only for the chat's own
// repository.
//
// Git is told about the helper through a global configuration file of the
// chat's own, <data>/chats/<id>/gitconfig, named by GIT_CONFIG_GLOBAL in the
// agent's environment. It includes the person's own global configuration (name,
// email, aliases) and then puts the helper in front of every other one for
// GitHub. A file, and not GIT_CONFIG_COUNT/KEY/VALUE, because Codex keeps
// variables whose names hold KEY or TOKEN out of the commands its agent runs.

// gitConfigName is the chat's global git configuration in its folder.
const gitConfigName = "gitconfig"

// withRepoGit is the CLI's environment with git pointed at the chat's own
// global configuration, written now, when the chat works in a repository;
// env as it is otherwise.
func (h *host) withRepoGit(env []string) []string {
	repo := h.spec.ChatTools.Repo
	if repo == nil {
		return env
	}
	exe, err := os.Executable()
	if err != nil {
		log.Printf("git credentials: %v", err)
		return env
	}
	path := filepath.Join(h.dir, gitConfigName)
	text := RepoGitConfig(env, repo.URL, exe, h.opts.DataDir, h.opts.ChatID)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		log.Printf("git credentials: %v", err)
		return env
	}
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, "GIT_CONFIG_GLOBAL=") {
			out = append(out, kv)
		}
	}
	return append(out, "GIT_CONFIG_GLOBAL="+path)
}

// RepoGitConfig is a chat's global git configuration: the person's own
// global configuration as env finds it, then the credential helper for the
// host of the repository's clone URL (cloneURL): the helpers before it are
// cleared, so that root's own login (gh) is not what the agent pushes with.
func RepoGitConfig(env []string, cloneURL, exe, dataDir, chatID string) string {
	get := func(key string) string {
		for _, kv := range env {
			if k, v, ok := strings.Cut(kv, "="); ok && k == key {
				return v
			}
		}
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Written by Socrates for the agent of chat %s: git's global configuration for it.\n", chatID)
	var includes []string
	if own := get("GIT_CONFIG_GLOBAL"); own != "" {
		includes = append(includes, own)
	} else if home := get("HOME"); home != "" {
		xdg := get("XDG_CONFIG_HOME")
		if xdg == "" {
			xdg = filepath.Join(home, ".config")
		}
		// git reads both, the XDG one first; a file that is not there is
		// skipped.
		includes = append(includes, filepath.Join(xdg, "git", "config"), filepath.Join(home, ".gitconfig"))
	}
	if len(includes) > 0 {
		b.WriteString("[include]\n")
		for _, path := range includes {
			fmt.Fprintf(&b, "\tpath = %s\n", configValue(path))
		}
	}
	if prefix := credentialPrefix(cloneURL); prefix != "" {
		helper := "!" + shellQuote(exe) + " git-credential --data " + shellQuote(dataDir) + " --chat " + shellQuote(chatID)
		fmt.Fprintf(&b, "[credential %s]\n\thelper =\n\thelper = %s\n\tuseHttpPath = true\n", configValue(prefix), configValue(helper))
	}
	return b.String()
}

// credentialPrefix is the scheme and host of a clone URL, which is what
// git matches a credential section against; "" for one that has none (a
// file:// remote asks for no credentials).
func credentialPrefix(cloneURL string) string {
	u, err := url.Parse(cloneURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// configValue is a value in git's configuration syntax: in double quotes,
// with its backslashes and quotes escaped.
func configValue(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// shellQuote is s as one word for sh, which runs a "!" helper.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// CredentialRequest is what `socrates git-credential` asks the server
// (chattools.Credential): git's description of the remote it needs
// credentials for.
type CredentialRequest struct {
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Path     string `json:"path"`
}

// GitCredentialMain is the `socrates git-credential [--data <dir> --chat
// <id>] <get|store|erase>` subcommand: a git credential helper. get reads
// git's request on stdin and answers with the token the server hands out for
// the chat's own repository; anything else the server refuses gets no
// answer, and git fails with its own message. store and erase do nothing: the
// token is the server's to keep. The endpoint is the environment's when it
// names one, else the chat's launch spec (host.json).
func GitCredentialMain(args []string) int {
	fs := flag.NewFlagSet("git-credential", flag.ContinueOnError)
	data := fs.String("data", "", "the data directory")
	chat := fs.String("chat", "", "the chat")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.Arg(0) != "get" {
		return 0
	}
	req := readCredentialRequest(os.Stdin)
	endpoint := chattools.FromEnv()
	if !endpoint.Usable() && *data != "" && ValidChatID(*chat) {
		if spec, err := ReadSpec(*data, *chat); err == nil {
			endpoint = spec.ChatTools
		}
	}
	answer, err := AskCredential(context.Background(), endpoint, req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "socrates: no credentials for %s://%s/%s: %v\n", req.Protocol, req.Host, req.Path, err)
		return 0
	}
	fmt.Fprint(os.Stdout, answer)
	return 0
}

// readCredentialRequest reads git's key=value lines up to the empty one.
func readCredentialRequest(in io.Reader) CredentialRequest {
	var req CredentialRequest
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break
		}
		key, value, _ := strings.Cut(line, "=")
		switch key {
		case "protocol":
			req.Protocol = value
		case "host":
			req.Host = value
		case "path":
			req.Path = value
		}
	}
	return req
}

// AskCredential asks the server for the token for req's repository and says
// it as git's credential helper answer.
func AskCredential(ctx context.Context, endpoint chattools.Endpoint, req CredentialRequest) (string, error) {
	if !endpoint.Usable() {
		return "", fmt.Errorf("this is not a chat of Socrates")
	}
	args, _ := json.Marshal(req)
	res := endpoint.Call(ctx, chattools.Credential, args)
	if res.Error {
		return "", fmt.Errorf("%s", res.Text)
	}
	token := strings.TrimSpace(res.Text)
	if token == "" || strings.ContainsAny(token, "\n\r") {
		return "", fmt.Errorf("the answer is not a token")
	}
	return "username=x-access-token\npassword=" + token + "\n", nil
}
