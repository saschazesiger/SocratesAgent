package driver

import (
	"context"
	"regexp"
	"strings"
)

// Mail is one mail of the mailbox.
type Mail struct {
	Received, From, To, Title string
	// Code is the mail's code ("" when it has none).
	Code  string
	Links []string
}

// MailSource is how a session reads the mailbox: the newest mails to the
// address received in the last sinceSeconds, at most limit of them.
type MailSource interface {
	Read(ctx context.Context, to string, sinceSeconds, limit int) ([]Mail, error)
}

var (
	linkPattern = regexp.MustCompile(`https?://[^\s\v\pZ\x{FEFF}"'<>)\]]+`)
	linkTail    = regexp.MustCompile(`[.,;]+$`)
	codePattern = regexp.MustCompile(`\b(\d{6,8})\b`)
)

// ExtractLinks returns the links a person would follow, unescaped, without
// duplicates, at most 10: those to one of hosts or its subdomains, or every
// http(s) link when hosts is empty.
func ExtractLinks(text, html string, hosts []string) []string {
	var allowed *regexp.Regexp
	if len(hosts) > 0 {
		quoted := make([]string, len(hosts))
		for i, h := range hosts {
			quoted[i] = regexp.QuoteMeta(h)
		}
		allowed = regexp.MustCompile(`(?i)^https?://([a-z0-9-]+\.)*(` + strings.Join(quoted, "|") + `)(/|$)`)
	}
	seen := map[string]bool{}
	var links []string
	for _, match := range linkPattern.FindAllString(text+"\n"+html, -1) {
		link := linkTail.ReplaceAllString(strings.ReplaceAll(match, "&amp;", "&"), "")
		if seen[link] {
			continue
		}
		seen[link] = true
		if allowed != nil && !allowed.MatchString(link) {
			continue
		}
		links = append(links, link)
		if len(links) == 10 {
			break
		}
	}
	return links
}

// FallbackCode is a six to eight digit code from a mail's subject or body,
// for when the mail analysis found none; "" when there is none.
func FallbackCode(text string) string {
	if m := codePattern.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}
