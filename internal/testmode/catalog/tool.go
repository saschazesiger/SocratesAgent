package catalog

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// toolName is the name of a tool's folder: it becomes part of the lease's
// variable names (SOCRATES_LEASE_<TOOL>) and of the run's paths.
var toolName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// secretName is a secret's name as a NAME=value line carries it.
var secretName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// loadTools reads every folder of tools/ with its TOOL.md. Files directly
// in tools/ (a README.md) are not tools.
func (c *Catalog) loadTools() {
	entries, err := os.ReadDir(filepath.Join(c.Dir, ToolsDir))
	if err != nil {
		c.errorf(ToolsDir+"/", 0, "%v", err)
		return
	}
	for _, e := range entries {
		name := e.Name()
		rel := ToolsDir + "/" + name + "/"
		switch {
		case strings.HasPrefix(name, "."):
			continue
		case e.Type()&os.ModeSymlink != 0:
			c.warnf(rel, 0, "a symbolic link is not a tool (it is not copied into a run)")
			continue
		case !e.IsDir():
			continue
		case !toolName.MatchString(name):
			c.errorf(rel, 0, "a tool's folder is its name: lower-case letters, digits and -")
			continue
		}
		if _, err := os.Stat(filepath.Join(c.Dir, ToolsDir, name, ToolFile)); err != nil {
			c.errorf(rel, 0, "a tool's folder needs a %s", ToolFile)
			continue
		}
		c.Tools[name] = c.loadTool(name)
	}
}

func (c *Catalog) loadTool(name string) *Tool {
	rel := ToolsDir + "/" + name + "/" + ToolFile
	t := &Tool{Name: name, Dir: filepath.Join(c.Dir, ToolsDir, name)}
	front, body, _, _ := c.readFile(rel)
	d := decoder{c: c, file: rel, m: front}
	d.keys("title", "bin", "setup", "end", "secrets", "pool")
	t.Title = d.str("title")
	if t.Title == "" {
		t.Title = name
	}
	t.Instructions = strings.TrimSpace(body)
	t.Setup = strings.TrimSpace(d.str("setup"))
	t.End = strings.TrimSpace(d.str("end"))

	t.Bin = d.str("bin")
	if t.Bin == "" {
		t.Bin = "bin"
	}
	clean := path.Clean(t.Bin)
	switch {
	case path.IsAbs(t.Bin) || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(t.Bin, `\`):
		d.errorf("bin", "bin is a folder inside the tool's own, not %q", t.Bin)
	default:
		t.Bin = clean
		if fi, err := os.Stat(filepath.Join(t.Dir, filepath.FromSlash(clean))); err != nil || !fi.IsDir() {
			d.errorf("bin", "the tool has no folder %s (bin: the folder put on PATH)", clean)
		}
	}

	if s, ok := d.sub("secrets"); ok {
		s.keys("command", "names")
		t.Secrets.Command = strings.TrimSpace(s.str("command"))
		for _, n := range s.list("names") {
			sn := SecretName{Name: strings.TrimSuffix(n, "?"), Optional: strings.HasSuffix(n, "?")}
			if !secretName.MatchString(sn.Name) {
				s.errorf("names", "secrets.names: %q is not a secret's name (letters, digits and _; a ? after it makes it optional)", n)
				continue
			}
			for _, have := range t.Secrets.Names {
				if have.Name == sn.Name {
					s.errorf("names", "secrets.names: %s is listed twice", sn.Name)
				}
			}
			t.Secrets.Names = append(t.Secrets.Names, sn)
		}
		if t.Secrets.Command == "" {
			s.errorf("command", "secrets.command is missing: the command that prints the NAME=value lines")
		}
		if len(t.Secrets.Names) == 0 {
			s.errorf("names", "secrets.names is missing: the secrets the tool takes (the command's other lines are dropped)")
		}
	}

	if p, ok := d.sub("pool"); ok {
		p.keys("secret", "separator", "per_member", "status")
		t.Pool = &Pool{
			Secret:    p.str("secret"),
			Separator: p.str("separator"),
			PerMember: p.positive("per_member", 1),
			Status:    strings.TrimSpace(p.str("status")),
		}
		if t.Pool.Separator == "" {
			t.Pool.Separator = ","
		}
		var named *SecretName
		for i := range t.Secrets.Names {
			if t.Secrets.Names[i].Name == t.Pool.Secret {
				named = &t.Secrets.Names[i]
			}
		}
		switch {
		case t.Pool.Secret == "":
			p.errorf("secret", "pool.secret is missing: the secret whose entries are the pool's members")
		case named == nil:
			p.errorf("secret", "pool.secret %s is not in secrets.names", t.Pool.Secret)
		case named.Optional:
			p.errorf("secret", "pool.secret %s is optional in secrets.names: a pool needs its members", t.Pool.Secret)
		}
	}
	return t
}

// MemberLabel is how the UI names a pool member: the name of a value of the
// form name:secret (BrowserStack's user:key), or "" when the value has no
// colon and the member goes by its number.
func (t *Tool) MemberLabel(value string) string {
	if name, _, ok := strings.Cut(value, ":"); ok {
		return name
	}
	return ""
}
