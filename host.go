package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// checkHost validates a server URL and returns it normalized as
// scheme://host[:port] (no path, query, fragment, or trailing slash).
// https is always allowed; plain http only for loopback hosts.
func checkHost(raw string) (string, error) {
	return checkHostAllowInsecure(raw, false)
}

// checkHostAllowInsecure is checkHost but also accepts plain http
// hosts when insecure is set (--insecure).
func checkHostAllowInsecure(raw string, insecure bool) (string, error) {
	if raw == "" {
		return "", errors.New("empty host")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" {
		u, err = url.Parse("https://" + raw)
		if err != nil {
			return "", err
		}
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !insecure && !isLoopbackHost(u.Hostname()) {
			return "", fmt.Errorf("refusing plain http host %q: use https", raw)
		}
	default:
		return "", fmt.Errorf("unsupported scheme %q in %q: use https", u.Scheme, raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("missing host in %q", raw)
	}
	if u.User != nil {
		return "", fmt.Errorf("userinfo not allowed in host %q", raw)
	}
	return u.Scheme + "://" + u.Host, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
