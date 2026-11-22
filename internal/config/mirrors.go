package config

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// RegistryMirror is one validated docker.registryMirrors entry.
type RegistryMirror struct {
	URL  string // normalized base URL, "http://10.255.255.1:5000"
	Host string // host[:port], as BuildKit and insecure-registries name it
	HTTP bool   // plain http (an insecure registry for dockerd, http = true for BuildKit)
}

// A registry host is a DNS name or an IPv4 address; together with the port check
// this keeps the value safe to write into daemon.json and buildkitd.toml.
var mirrorHostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$`)

// ParseRegistryMirror validates one mirror: an http:// or https:// base URL with a
// host and optional port, and nothing else (no path, query, credentials).
func ParseRegistryMirror(s string) (RegistryMirror, error) {
	raw := strings.TrimSpace(s)
	u, err := url.Parse(raw)
	if err != nil {
		return RegistryMirror{}, fmt.Errorf("registry mirror %q: %w", s, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return RegistryMirror{}, fmt.Errorf("registry mirror %q: want http:// or https://", s)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return RegistryMirror{}, fmt.Errorf("registry mirror %q: only scheme://host[:port], no path, query or credentials", s)
	}
	host, port := u.Hostname(), u.Port()
	if !mirrorHostRe.MatchString(host) || strings.Contains(u.Host, "[") {
		return RegistryMirror{}, fmt.Errorf("registry mirror %q: invalid host", s)
	}
	if port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return RegistryMirror{}, fmt.Errorf("registry mirror %q: invalid port", s)
		}
		host = net.JoinHostPort(host, port)
	}
	return RegistryMirror{URL: u.Scheme + "://" + host, Host: host, HTTP: u.Scheme == "http"}, nil
}

// ParseRegistryMirrors validates every entry, returning the usable mirrors in
// order (duplicates dropped) and one message per entry it leaves out. A bad entry
// costs only that mirror: the rest still apply, and none stops a slot.
func ParseRegistryMirrors(list []string) (mirrors []RegistryMirror, problems []string) {
	seen := map[string]bool{}
	for _, s := range list {
		m, err := ParseRegistryMirror(s)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if seen[m.Host] {
			continue
		}
		seen[m.Host] = true
		mirrors = append(mirrors, m)
	}
	return mirrors, problems
}
