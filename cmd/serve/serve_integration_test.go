//go:build integration

package serve_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/StevenACoffman/roster/cmd"
	"github.com/StevenACoffman/roster/internal/testutil"
)

// End-to-end: drive the real dispatcher, over a real listener, against a real
// database, as an ordinary HTTP client. Nothing here reaches into the handler.
//
// An external test package driving cmd.Run rather than serve.exec, because
// cmd.Run is the seam main() itself uses — testing below it would leave the
// dispatcher, flag parsing, and env mapping unexercised.

// timeMultiplier lets a slow CI machine widen every timeout at once.
const timeMultiplier = 1

// listeningRE matches the address line serve prints to stdout once bound. That
// line exists so a caller asking for port 0 can discover the port it got, and
// this is the caller it was written for.
var listeningRE = regexp.MustCompile(`listening on (https?://\S+)`)

func ok(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func equals[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// startServer runs `roster serve` on an OS-assigned port and returns its base
// URL once it answers /readyz.
//
// Returns no error: a helper reports its own failures. The server stops when the
// test ends, via the context it was given.
func startServer(t *testing.T, databaseURL string, extraArgs ...string) string {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	// A pipe rather than a buffer: the address has to be readable while the
	// server keeps running, and a buffer written from another goroutine would be
	// a data race.
	stdoutR, stdoutW := io.Pipe()
	t.Cleanup(func() { _ = stdoutW.Close() })

	args := append([]string{
		"serve",
		// Port 0: the OS picks a free port, so parallel tests cannot collide.
		"--addr", "127.0.0.1:0",
		"--database-url", databaseURL,
		"--dev-subject", "oidc|dee",
		"--admin-addr", "off",
	}, extraArgs...)

	done := make(chan error, 1)
	go func() {
		err := cmd.Run(ctx, args, strings.NewReader(""), stdoutW, io.Discard)
		_ = stdoutW.Close()
		done <- err
	}()

	// Read the resolved address off stdout.
	addrCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdoutR)
		for scanner.Scan() {
			if m := listeningRE.FindStringSubmatch(scanner.Text()); m != nil {
				addrCh <- m[1]
				return
			}
		}
		close(addrCh)
	}()

	var base string
	select {
	case addr, open := <-addrCh:
		if !open {
			t.Fatal("server exited before printing its address")
		}
		base = addr
	case err := <-done:
		t.Fatalf("server exited during startup: %v", err)
	case <-time.After(30 * time.Second * timeMultiplier):
		t.Fatal("timed out waiting for the server to bind")
	}

	waitForReady(ctx, t, base+"/readyz")

	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			// A cancelled context is the shutdown path, not a failure.
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("server exited with %v", err)
			}
		case <-time.After(30 * time.Second * timeMultiplier):
			t.Error("server did not shut down within the drain timeout")
		}
	})

	return base
}

// waitForReady polls endpoint until it answers 200.
//
// Polls on a ticker rather than sleeping: a fixed sleep is both slower than
// necessary when the server is already up and unreliable when it is not.
func waitForReady(ctx context.Context, t *testing.T, endpoint string) {
	t.Helper()

	const (
		timeout  = 30 * time.Second * timeMultiplier
		interval = 25 * time.Millisecond * timeMultiplier
	)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	deadline := time.After(timeout)

	client := &http.Client{Timeout: 2 * time.Second * timeMultiplier}
	var lastErr error

	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
		ok(t, err)

		resp, err := client.Do(req)
		if err == nil {
			code := resp.StatusCode
			_ = resp.Body.Close()
			if code == http.StatusOK {
				return
			}
			lastErr = fmt.Errorf("status %d", code)
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			t.Fatalf("context cancelled waiting for %s: %v", endpoint, ctx.Err())
		case <-deadline:
			t.Fatalf("timed out waiting for %s (last: %v)", endpoint, lastErr)
		case <-ticker.C:
		}
	}
}

// post issues one Connect/JSON call and returns the status and decoded body.
func post(t *testing.T, base, method, body string, headers map[string]string) (status int, decoded map[string]any) {
	t.Helper()

	url := base + "/oneroster.v1p2.v1.RosterService/" + method
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(body))
	ok(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	ok(t, err)
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	ok(t, err)

	decoded = map[string]any{}
	if len(raw) > 0 {
		ok(t, json.Unmarshal(raw, &decoded))
	}
	return resp.StatusCode, decoded
}

func TestServeAnswersHealthAndReadiness(t *testing.T) {
	t.Parallel()

	base := startServer(t, testutil.NewDatabaseURL(t))

	for _, path := range []string{"/healthz", "/readyz"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+path, http.NoBody)
		ok(t, err)
		resp, err := http.DefaultClient.Do(req)
		ok(t, err)
		code := resp.StatusCode
		_ = resp.Body.Close()
		equals(t, code, http.StatusOK)
	}
}

func TestServeReturns404ForAnUnknownPath(t *testing.T) {
	t.Parallel()

	base := startServer(t, testutil.NewDatabaseURL(t))

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/nope", http.NoBody)
	ok(t, err)
	resp, err := http.DefaultClient.Do(req)
	ok(t, err)
	code := resp.StatusCode
	_ = resp.Body.Close()
	equals(t, code, http.StatusNotFound)
}

func TestServeAnswersAnRPCOverJSON(t *testing.T) {
	t.Parallel()

	base := startServer(t, testutil.NewDatabaseURL(t))

	// An empty database, so the interesting assertion is that the call succeeds
	// and the repeated field renders as [] rather than null.
	status, body := post(t, base, "GetAllOrgs", `{}`, nil)
	equals(t, status, http.StatusOK)
	if _, present := body["nextPageToken"]; present {
		t.Errorf("empty result carried a next page token: %v", body)
	}
}

func TestServeRejectsInvalidRequestsAtTheEdge(t *testing.T) {
	t.Parallel()

	base := startServer(t, testutil.NewDatabaseURL(t))

	tests := []struct {
		name   string
		method string
		body   string
	}{
		{name: "page size above the declared maximum", method: "GetAllOrgs", body: `{"pageSize":99999}`},
		{name: "negative page size", method: "GetAllOrgs", body: `{"pageSize":-1}`},
		{name: "blank sourced id", method: "GetOrg", body: `{"sourcedId":""}`},
		{name: "malformed page token", method: "GetAllOrgs", body: `{"pageToken":"!!!"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			status, body := post(t, base, tt.method, tt.body, nil)
			equals(t, status, http.StatusBadRequest)
			equals(t, body["code"], "invalid_argument")
		})
	}
}

func TestServeRefusesDevSubjectOffLoopback(t *testing.T) {
	t.Parallel()

	// --dev-subject disables authentication, so binding it to every interface
	// would serve the roster to anyone who asked. The command must refuse rather
	// than start.
	err := cmd.Run(t.Context(), []string{
		"serve", "--addr", "0.0.0.0:0", "--dev-subject", "oidc|dee",
	}, strings.NewReader(""), io.Discard, io.Discard)

	if err == nil {
		t.Fatal("serve started with --dev-subject on a non-loopback address")
	}
	if !strings.Contains(err.Error(), "loopback") {
		t.Errorf("got %v, want an error explaining the loopback requirement", err)
	}
}

func TestServeRequiresBothTLSFilesTogether(t *testing.T) {
	t.Parallel()

	err := cmd.Run(t.Context(), []string{
		"serve", "--addr", "127.0.0.1:0", "--tls-cert", "cert.pem",
	}, strings.NewReader(""), io.Discard, io.Discard)

	if err == nil {
		t.Fatal("serve started with only one half of the TLS pair")
	}
	if !strings.Contains(err.Error(), "--tls-key") {
		t.Errorf("got %v, want an error naming the missing flag", err)
	}
}
