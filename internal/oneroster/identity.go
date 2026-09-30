package oneroster

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// Resolving who a request claims to be, as a total function of its headers.
//
// Nothing here reads a database, a clock, or a config file, which is what lets
// every proxy deployment be exercised by a unit test rather than a container.
// Whether to believe the answer is the shell's decision, in auth.go.
//
// Roles are deliberately absent. X-Forwarded-Groups is read by nothing here:
// roster's roles come from auth_grant and reach a query through
// auth_effective_access, so a group list asserted by a header would be a second
// source of truth able to drift from the one the database enforces.

// Provider names, recorded so an operator reading a log line can tell which path
// admitted a caller.
const (
	providerIAP   = "iap"
	providerProxy = "oauth2-proxy"
)

// The headers each upstream sets.
const (
	// Google IAP. The assertion is the signed one; the other two are convenience
	// copies IAP derives from it.
	headerIAPEmail     = "X-Goog-Authenticated-User-Email"
	headerIAPID        = "X-Goog-Authenticated-User-Id"
	headerIAPAssertion = "X-Goog-IAP-JWT-Assertion"

	// oauth2-proxy.
	headerForwardedUser  = "X-Forwarded-User"
	headerForwardedEmail = "X-Forwarded-Email"
)

// iapPrefix is what IAP prepends to both the id and the email it forwards.
const iapPrefix = "accounts.google.com:"

// identity is who an upstream proxy says is calling.
type identity struct {
	// subject is the join key against auth_principal. Never empty when
	// proxyIdentity reported one.
	subject string

	// provider names the header set that supplied it, for logging.
	provider string

	// assertion is IAP's signed JWT, when it sent one. The shell hands it to a
	// configured validator; nothing here inspects it.
	assertion string
}

// proxyIdentity reports the identity a trusted upstream asserted, if any.
//
// Precedence is IAP, then oauth2-proxy, matching petstore-reference: a request
// carrying both came through IAP with something else behind it, and the signed
// assertion is the stronger claim.
//
// With trustProxy false every one of these headers is ignored rather than
// refused. Refusing would let a prober tell a proxied deployment from a
// directly reachable one, and ignoring is what makes the flag an actual
// boundary: on a listener a client can reach directly, anyone could set
// X-Forwarded-User and become anyone.
//
// Ensures: when ok, subject is non-empty. Total — no header value produces an
// error or a panic.
func proxyIdentity(h http.Header, trustProxy bool) (identity, bool) {
	if !trustProxy {
		return identity{}, false
	}

	assertion := h.Get(headerIAPAssertion)
	if subject := firstNonEmpty(
		strings.TrimPrefix(h.Get(headerIAPID), iapPrefix),
		strings.TrimPrefix(h.Get(headerIAPEmail), iapPrefix),
	); subject != "" {
		return identity{subject: subject, provider: providerIAP, assertion: assertion}, true
	}

	if subject := firstNonEmpty(
		h.Get(headerForwardedUser),
		h.Get(headerForwardedEmail),
	); subject != "" {
		return identity{subject: subject, provider: providerProxy}, true
	}

	// Reached when the headers name nobody, which includes the malformed case of
	// an IAP assertion with no accompanying id or email. Reported as no identity
	// rather than as an error, so the caller falls through to the bearer path and
	// hears one message about credentials instead of a hint about which upstream
	// is deployed.
	return identity{}, false
}

// firstNonEmpty returns the first argument that is not blank once trimmed.
//
// Trimming matters because a proxy that sets a header it has no value for emits
// an empty or whitespace one rather than omitting it, and a subject of " " would
// authenticate as a principal that cannot exist.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// bearerToken extracts the credential from an Authorization header.
//
// The scheme is compared in constant time along with its length, so the
// comparison leaks nothing about how close a malformed header was to correct.
func bearerToken(authorization string) (string, error) {
	const prefix = "Bearer "
	if len(authorization) <= len(prefix) {
		return "", errMissingCredential
	}
	if subtle.ConstantTimeCompare([]byte(authorization[:len(prefix)]), []byte(prefix)) != 1 {
		return "", errMissingCredential
	}

	token := strings.TrimSpace(authorization[len(prefix):])
	if token == "" {
		return "", errMissingCredential
	}
	return token, nil
}
