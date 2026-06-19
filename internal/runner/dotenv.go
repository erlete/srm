package runner

import (
	"fmt"
	"sort"
	"strings"
)

// ProxyConfig holds the runner-directory .env proxy settings. Per GitHub, the
// NoProxy list supports hostnames only (no IP addresses).
type ProxyConfig struct {
	HTTPProxy  string
	HTTPSProxy string
	NoProxy    string
}

// RenderDotEnv builds the contents of a runner-directory .env file from proxy
// settings plus extra KEY=VALUE entries (sorted for stable output).
func RenderDotEnv(p ProxyConfig, extra map[string]string) string {
	var b strings.Builder
	if p.HTTPProxy != "" {
		fmt.Fprintf(&b, "http_proxy=%s\n", p.HTTPProxy)
	}
	if p.HTTPSProxy != "" {
		fmt.Fprintf(&b, "https_proxy=%s\n", p.HTTPSProxy)
	}
	if p.NoProxy != "" {
		fmt.Fprintf(&b, "no_proxy=%s\n", p.NoProxy)
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, extra[k])
	}
	return b.String()
}
