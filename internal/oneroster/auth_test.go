package oneroster

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

// The Authenticator decides whom to believe. These run without a container
// because every path except the token table is reachable without one, which is
// the point of keeping the resolution pure and the lookup last.

// staticValidator stands in for a real IAP or OIDC verifier. A closure rather
// than a mock type: the contract is one function.
func staticValidator(subject string, err error) TokenValidator {
	return func(context.Context, string) (string, error) { return subject, err }
}

func TestAuthenticateResolvesCredentials(t *testing.T) {
	t.Parallel()

	errRejected := errors.New("rejected")

	tests := []struct {
		name        string
		opts        []AuthenticatorOption
		header      http.Header
		wantSubject string
		wantCode    connect.Code
		wantErrText string
	}{
		{
			name:        "a trusted forwarded user authenticates with no database",
			opts:        []AuthenticatorOption{WithTrustedProxyHeaders()},
			header:      headers(headerForwardedUser, "ada"),
			wantSubject: "ada",
		},
		{
			name:        "a trusted IAP id authenticates with no database",
			opts:        []AuthenticatorOption{WithTrustedProxyHeaders()},
			header:      headers(headerIAPID, iapPrefix+"12345"),
			wantSubject: "12345",
		},
		{
			// The security boundary. Without the flag the headers are inert, so
			// the request falls through to the bearer path and is refused for
			// carrying no credential at all.
			name:     "an untrusted forwarded user is refused, not believed",
			header:   headers(headerForwardedUser, "ada"),
			wantCode: connect.CodeUnauthenticated,
		},
		{
			name:     "no credentials at all",
			header:   headers(),
			wantCode: connect.CodeUnauthenticated,
		},
		{
			name: "a validator verifies an IAP assertion and names the subject",
			opts: []AuthenticatorOption{
				WithTrustedProxyHeaders(),
				WithTokenValidator(staticValidator("verified-subject", nil)),
			},
			header: headers(
				headerIAPID, iapPrefix+"12345",
				headerIAPAssertion, "signed-jwt",
			),
			wantSubject: "verified-subject",
		},
		{
			// An operator who configured verification gets it or gets an error.
			// Falling back to the plain headers would be a silent downgrade.
			name: "IAP without its assertion is refused once a validator is configured",
			opts: []AuthenticatorOption{
				WithTrustedProxyHeaders(),
				WithTokenValidator(staticValidator("unused", nil)),
			},
			header:      headers(headerIAPID, iapPrefix+"12345"),
			wantCode:    connect.CodeUnauthenticated,
			wantErrText: "no signed assertion",
		},
		{
			// Regression: an oauth2-proxy identity has no assertion by
			// definition. Demanding one would reject every request behind an
			// oauth2-proxy the moment a validator was configured for IAP.
			name: "oauth2-proxy is trusted directly even with a validator configured",
			opts: []AuthenticatorOption{
				WithTrustedProxyHeaders(),
				WithTokenValidator(staticValidator("should-not-be-used", nil)),
			},
			header:      headers(headerForwardedUser, "ada"),
			wantSubject: "ada",
		},
		{
			name: "a validator rejecting an assertion refuses the request",
			opts: []AuthenticatorOption{
				WithTrustedProxyHeaders(),
				WithTokenValidator(staticValidator("", errRejected)),
			},
			header: headers(
				headerIAPID, iapPrefix+"12345",
				headerIAPAssertion, "signed-jwt",
			),
			wantCode: connect.CodeUnauthenticated,
		},
		{
			// A deployment verifying OIDC tokens has no rows to look them up in,
			// so the validator takes precedence over the token table.
			name:        "a validator resolves a bearer token without the token table",
			opts:        []AuthenticatorOption{WithTokenValidator(staticValidator("oidc-subject", nil))},
			header:      headers("Authorization", "Bearer some-jwt"),
			wantSubject: "oidc-subject",
		},
		{
			name:     "a validator rejecting a bearer token refuses the request",
			opts:     []AuthenticatorOption{WithTokenValidator(staticValidator("", errRejected))},
			header:   headers("Authorization", "Bearer some-jwt"),
			wantCode: connect.CodeUnauthenticated,
		},
		{
			// No pool and no validator leaves nothing able to resolve a token.
			// Unavailable, not Unauthenticated: the credential was never judged.
			name:     "a bearer token with no way to resolve it is unavailable",
			header:   headers("Authorization", "Bearer some-token"),
			wantCode: connect.CodeUnavailable,
		},
		{
			name:     "a malformed authorization scheme carries no credential",
			header:   headers("Authorization", "Basic dXNlcjpwYXNz"),
			wantCode: connect.CodeUnauthenticated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// A nil pool: every case here resolves without the token table, and
			// the one that cannot asserts exactly that.
			subject, err := NewAuthenticator(nil, tt.opts...).authenticate(t.Context(), tt.header)

			if tt.wantSubject != "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if subject != tt.wantSubject {
					t.Errorf("subject = %q, want %q", subject, tt.wantSubject)
				}

				return
			}

			if err == nil {
				t.Fatalf("authenticated as %q, want an error", subject)
			}
			if got := connect.CodeOf(err); got != tt.wantCode {
				t.Errorf("code = %v, want %v (error %v)", got, tt.wantCode, err)
			}
			if tt.wantErrText != "" && !strings.Contains(err.Error(), tt.wantErrText) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantErrText)
			}
		})
	}
}

// TestAuthenticateRejectsAnEmptySubjectFromAValidator guards the invariant the
// rest of the service depends on. An empty subject matches no row in
// auth_effective_access, so it would not leak data, but the handler would report
// it as unauthenticated far from the validator that produced it.
func TestAuthenticateRejectsAnEmptySubjectFromAValidator(t *testing.T) {
	t.Parallel()

	subject, err := NewAuthenticator(nil,
		WithTokenValidator(staticValidator("", nil)),
	).authenticate(t.Context(), headers("Authorization", "Bearer token"))
	if err == nil {
		t.Fatalf("a validator returning no subject authenticated as %q", subject)
	}
	if got := connect.CodeOf(err); got != connect.CodeUnauthenticated {
		t.Errorf("code = %v, want %v", got, connect.CodeUnauthenticated)
	}
}
