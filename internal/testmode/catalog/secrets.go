package catalog

import "strings"

// ParseSecrets reads what a tool's secrets command printed: NAME=value
// lines, as `secretctl show` prints them and qa's secrets.ts read them. A
// line is split at its first "=" and the value taken raw, without
// unquoting (a real secret may hold quotes) and without trimming; an
// "export " in front is dropped. Blank lines, # comments and lines without
// a valid name are skipped. The last line of a name wins.
func ParseSecrets(out []byte) map[string]string {
	secrets := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		line = strings.TrimLeft(line, " \t")
		if rest, ok := strings.CutPrefix(line, "export "); ok {
			line = strings.TrimLeft(rest, " \t")
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok || !secretName.MatchString(name) {
			continue
		}
		secrets[name] = value
	}
	return secrets
}
