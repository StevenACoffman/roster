package oneroster

import (
	"errors"
	"net/http"
	"testing"
)

// The security boundary this file tests is one line in proxyIdentity: with
// trustProxy false, no header can name a caller. On a listener a client reaches
// directly, an X-Forwarded-User that was believed would let anyone become
// anyone, so the untrusted cases below matter more than the trusted ones.

func headers(pairs ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		h.Set(pairs[i], pairs[i+1])
	}
	return h
}

func TestProxyIdentityIgnoresEveryHeaderWhenUntrusted(t *testing.T) {
	t.Parallel()

	// Each of these authenticates when the flag is on, which is what makes them
	// the right cases to assert are inert when it is off.
	tests := []struct {
		name string
		h    http.Header
	}{
		{name: "no headers at all", h: headers()},
		{
			name: "an IAP id",
			h:    headers(headerIAPID, iapPrefix+"12345"),
		},
		{
			name: "an IAP email and assertion",
			h:    headers(headerIAPEmail, iapPrefix+"ada@example.test", headerIAPAssertion, "jwt"),
		},
		{
			name: "a forwarded user",
			h:    headers(headerForwardedUser, "ada"),
		},
		{
			name: "a forwarded email",
			h:    headers(headerForwardedEmail, "ada@example.test"),
		},
		{
			name: "every header at once",
			h: headers(
				headerIAPID, iapPrefix+"12345",
				headerIAPEmail, iapPrefix+"ada@example.test",
				headerIAPAssertion, "jwt",
				headerForwardedUser, "ada",
				headerForwardedEmail, "ada@example.test",
			),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got, ok := proxyIdentity(tt.h, false); ok {
				t.Errorf("proxyIdentity trusted %q with trustProxy false; "+
					"a directly reachable listener would let any caller become anyone",
					got.subject)
			}
		})
	}
}

func TestProxyIdentityResolvesATrustedUpstream(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		h             http.Header
		wantSubject   string
		wantProvider  string
		wantAssertion string
		wantOK        bool
	}{
		{
			name:         "an IAP id, with the prefix stripped",
			h:            headers(headerIAPID, iapPrefix+"12345"),
			wantSubject:  "12345",
			wantProvider: providerIAP,
			wantOK:       true,
		},
		{
			name:          "an IAP id carries its assertion through for a validator",
			h:             headers(headerIAPID, iapPrefix+"12345", headerIAPAssertion, "signed-jwt"),
			wantSubject:   "12345",
			wantProvider:  providerIAP,
			wantAssertion: "signed-jwt",
			wantOK:        true,
		},
		{
			// IAP sends the id and the email. The id is the stable one: an email
			// can be reassigned to a different person.
			name: "the IAP id wins over the IAP email",
			h: headers(
				headerIAPID, iapPrefix+"12345",
				headerIAPEmail, iapPrefix+"ada@example.test",
			),
			wantSubject:  "12345",
			wantProvider: providerIAP,
			wantOK:       true,
		},
		{
			name:         "an IAP email alone still identifies the caller",
			h:            headers(headerIAPEmail, iapPrefix+"ada@example.test"),
			wantSubject:  "ada@example.test",
			wantProvider: providerIAP,
			wantOK:       true,
		},
		{
			name:         "a forwarded user",
			h:            headers(headerForwardedUser, "ada"),
			wantSubject:  "ada",
			wantProvider: providerProxy,
			wantOK:       true,
		},
		{
			name:         "a forwarded email when there is no user",
			h:            headers(headerForwardedEmail, "ada@example.test"),
			wantSubject:  "ada@example.test",
			wantProvider: providerProxy,
			wantOK:       true,
		},
		{
			// A request that reached IAP and an oauth2-proxy came through IAP
			// last, and its assertion is the signed claim.
			name: "IAP outranks oauth2-proxy",
			h: headers(
				headerIAPID, iapPrefix+"12345",
				headerForwardedUser, "someone-else",
			),
			wantSubject:  "12345",
			wantProvider: providerIAP,
			wantOK:       true,
		},
		{
			name:   "no identity headers at all",
			h:      headers(),
			wantOK: false,
		},
		{
			// A proxy with no value for a header sends it empty rather than
			// omitting it. A subject of " " would authenticate as a principal
			// that cannot exist, so it must not count as an identity.
			name:   "blank header values are not an identity",
			h:      headers(headerForwardedUser, "   ", headerIAPID, ""),
			wantOK: false,
		},
		{
			// An assertion with nothing to name is malformed. It falls through
			// rather than erroring, so the caller hears one message about
			// credentials instead of learning which upstream is deployed.
			name:   "an assertion with no subject is not an identity",
			h:      headers(headerIAPAssertion, "signed-jwt"),
			wantOK: false,
		},
		{
			name:         "a bare prefix leaves nothing to identify",
			h:            headers(headerIAPID, iapPrefix, headerForwardedUser, "fallback"),
			wantSubject:  "fallback",
			wantProvider: providerProxy,
			wantOK:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := proxyIdentity(tt.h, true)

			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (identity %+v)", ok, tt.wantOK, got)
			}
			if !ok {
				return
			}
			if got.subject != tt.wantSubject {
				t.Errorf("subject = %q, want %q", got.subject, tt.wantSubject)
			}
			if got.provider != tt.wantProvider {
				t.Errorf("provider = %q, want %q", got.provider, tt.wantProvider)
			}
			if got.assertion != tt.wantAssertion {
				t.Errorf("assertion = %q, want %q", got.assertion, tt.wantAssertion)
			}
		})
	}
}

// TestBearerToken covers the parsing directly rather than through the
// authenticator, because the boundary cases are where it matters: a header one
// byte short of a real one, and a scheme that differs only in case.
func TestBearerToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		header    string
		wantToken string
		wantErr   bool
	}{
		{name: "a token", header: "Bearer abc123", wantToken: "abc123"},
		{
			// Surrounding space is trimmed, so a header a client padded still
			// hashes to the same value as the token it issued.
			name: "surrounding space is trimmed", header: "Bearer   abc123  ", wantToken: "abc123",
		},
		{
			// The shortest header carrying a credential is the prefix plus one
			// byte. The prefix alone has nothing after it.
			name: "the prefix alone carries no credential", header: "Bearer ", wantErr: true,
		},
		{name: "a one-character token is still a token", header: "Bearer x", wantToken: "x"},
		{name: "only whitespace after the prefix", header: "Bearer      ", wantErr: true},
		{name: "empty", header: "", wantErr: true},
		{
			// RFC 7235 makes the scheme case-insensitive, but roster issues its
			// own tokens and compares the scheme in constant time, which a
			// case-folding comparison could not do.
			name: "a lowercase scheme is refused", header: "bearer abc123", wantErr: true,
		},
		{name: "another scheme entirely", header: "Basic dXNlcjpwYXNz", wantErr: true},
		{name: "the scheme without its space", header: "Bearerabc123", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := bearerToken(tt.header)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("bearerToken(%q) = %q, want an error", tt.header, got)
				}
				if !errors.Is(err, errMissingCredential) {
					t.Errorf("error = %v, want one wrapping errMissingCredential", err)
				}

				return
			}

			if err != nil {
				t.Fatalf("bearerToken(%q): %v", tt.header, err)
			}
			if got != tt.wantToken {
				t.Errorf("bearerToken(%q) = %q, want %q", tt.header, got, tt.wantToken)
			}
		})
	}
}
