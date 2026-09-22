// Command healthcheck probes an HTTP endpoint and exits non-zero when it is not
// healthy.
//
// It exists because the runtime images contain no shell and no curl: a container
// health check has to be a binary, and shipping one that is a few hundred
// kilobytes of Go is cheaper than shipping a package manager.
package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"
)

const timeout = 3 * time.Second

func main() {
	os.Exit(probe(os.Args[1:]))
}

// probe returns the exit code, so every deferred cleanup runs before the process
// ends.
func probe(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: healthcheck <url>")
		return 2
	}

	target, err := url.Parse(args[0])
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") {
		fmt.Fprintln(os.Stderr, "healthcheck: the argument must be an http or https url")
		return 2
	}
	// The probe only ever talks to the container it runs in, so the host is
	// pinned rather than taken from the argument. Without this, a health check
	// command in a compose file becomes a way to make the container issue
	// requests to anywhere it can reach.
	if target.Hostname() != "127.0.0.1" && target.Hostname() != "localhost" && target.Hostname() != "::1" {
		fmt.Fprintln(os.Stderr, "healthcheck: the url must address this container")
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// The url is rebuilt from the parts that were just validated, so nothing from
	// the argument reaches the request untouched.
	probeURL := (&url.URL{Scheme: target.Scheme, Host: target.Host, Path: target.Path}).String()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode < 200 || response.StatusCode > 299 {
		fmt.Fprintf(os.Stderr, "healthcheck: status %d\n", response.StatusCode)
		return 1
	}
	return 0
}
