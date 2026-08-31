package internet

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// staticResolver stands in for DNS, so a test can have "evil.example" answer
// with 127.0.0.1 without owning a domain.
func staticResolver(table map[string][]string) func(context.Context, string) ([]net.IP, error) {
	return func(_ context.Context, host string) ([]net.IP, error) {
		addrs, ok := table[host]
		if !ok {
			return nil, fmt.Errorf("no such host: %s", host)
		}
		out := make([]net.IP, 0, len(addrs))
		for _, a := range addrs {
			out = append(out, net.ParseIP(a))
		}
		return out, nil
	}
}

// hostsOf exempts one httptest server, by its exact host and port, from the
// address check. Nothing else on loopback is exempted with it.
func hostsOf(servers ...*httptest.Server) map[string]bool {
	out := map[string]bool{}
	for _, s := range servers {
		if u, err := url.Parse(s.URL); err == nil {
			out[u.Host] = true
		}
	}
	return out
}

func TestBlockedIPCoversEveryPrivateRange(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
	}{
		{"127.0.0.1", true},
		{"127.1.2.3", true},
		{"0.0.0.0", true},
		{"10.1.2.3", true},
		{"172.16.5.9", true},
		{"172.31.255.255", true},
		{"192.168.1.1", true},
		{"169.254.169.254", true}, // the cloud metadata service
		{"169.254.0.1", true},
		{"100.64.0.1", true}, // CGNAT
		{"100.127.255.255", true},
		{"224.0.0.1", true},
		{"255.255.255.255", true},
		{"::1", true},
		{"fc00::1", true},
		{"fd12:3456::1", true},
		{"fe80::1", true},
		{"::ffff:127.0.0.1", true}, // IPv4 loopback wearing an IPv6 hat
		{"::ffff:169.254.169.254", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"93.184.216.34", false},
		{"172.32.0.1", false}, // just outside RFC1918
		{"100.128.0.1", false},
		{"2606:4700:4700::1111", false},
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("%s is not an IP", c.ip)
		}
		if got := blockedIP(ip); got != c.blocked {
			t.Errorf("blockedIP(%s) = %v, want %v", c.ip, got, c.blocked)
		}
	}
}

func TestCheckURLRejectsPrivateNamesAndSchemes(t *testing.T) {
	guard := &Guard{Resolve: staticResolver(map[string][]string{
		"evil.example":     {"127.0.0.1"},
		"metadata.example": {"169.254.169.254"},
		"mixed.example":    {"93.184.216.34", "10.0.0.1"},
		"good.example":     {"93.184.216.34"},
	})}
	cases := []struct {
		raw      string
		wantFail bool
	}{
		{"https://good.example/page", false},
		{"http://good.example/page", false},
		{"https://evil.example/", true},
		{"https://metadata.example/latest/meta-data/", true},
		{"https://mixed.example/", true}, // one private answer is enough
		{"http://127.0.0.1:8080/api/settings", true},
		{"http://[::1]:8080/", true},
		{"http://169.254.169.254/latest/meta-data/", true},
		{"file:///etc/passwd", true},
		{"gopher://good.example/", true},
		{"https://nowhere.example/", true}, // resolves to nothing
	}
	for _, c := range cases {
		u, err := url.Parse(c.raw)
		if err != nil {
			t.Fatalf("parse %s: %v", c.raw, err)
		}
		err = guard.CheckURL(context.Background(), u)
		if c.wantFail && err == nil {
			t.Errorf("CheckURL(%s) allowed it, wanted a refusal", c.raw)
		}
		if !c.wantFail && err != nil {
			t.Errorf("CheckURL(%s) refused it: %v", c.raw, err)
		}
	}
}

// A redirect is the interesting case: the first hop is a perfectly ordinary
// public page and the second one is not.
func TestRedirectToAPrivateAddressIsRefused(t *testing.T) {
	for _, target := range []string{"http://127.0.0.1:9/secret", "http://169.254.169.254/latest/meta-data/"} {
		t.Run(target, func(t *testing.T) {
			var hops int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hops++
				http.Redirect(w, r, target, http.StatusFound)
			}))
			defer server.Close()

			// Only the test's own server is exempt. The redirect target goes
			// through the guard exactly as it would in production.
			guard := &Guard{allowHosts: hostsOf(server)}
			resp, err := guard.Client().Get(server.URL)
			if err == nil {
				resp.Body.Close()
				t.Fatalf("the redirect to %s was followed", target)
			}
			if !strings.Contains(err.Error(), "refusing to fetch") {
				t.Fatalf("wrong refusal for %s: %v", target, err)
			}
			if hops != 1 {
				t.Fatalf("the first hop ran %d times, wanted 1", hops)
			}
		})
	}
}

func TestTooManyRedirectsGivesUp(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, server.URL+"/again", http.StatusFound)
	}))
	defer server.Close()

	guard := &Guard{allowHosts: hostsOf(server)}
	resp, err := guard.Client().Get(server.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("an endless redirect loop was followed to the end")
	}
	if !strings.Contains(err.Error(), "redirects") {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestDialContextRefusesAPrivateAnswer(t *testing.T) {
	guard := &Guard{Resolve: staticResolver(map[string][]string{
		"evil.example": {"127.0.0.1"},
	})}
	if _, err := guard.DialContext(context.Background(), "tcp", "evil.example:443"); err == nil {
		t.Fatal("dialled a name that resolves to loopback")
	} else if !strings.Contains(err.Error(), "refusing to fetch") {
		t.Fatalf("wrong error: %v", err)
	}
}
