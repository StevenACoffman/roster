package oneroster

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/StevenACoffman/roster/internal/db"
)

// Authentication: establish who is calling, and nothing more.
//
// This interceptor makes no authorization decision. It does not consult roles,
// orgs, or grants — it resolves a credential to a subject, puts it on the
// context, and returns. Whether that subject may see a given row is decided by
// each query's join against auth_effective_access.
//
// Splitting it this way means a revoked grant takes effect on the next request
// rather than when a cached permission set expires, and it keeps the
// interceptor from having a policy that could drift from the one in SQL.

var (
	errMissingCredential = errors.New("no credentials presented")
	errBadCredential     = errors.New("bearer token is not recognized")
	errUnsignedAssertion = errors.New("proxy identity carries no signed assertion to verify")
)

// TokenValidator verifies a credential a proxy or client presented and reports
// the subject it belongs to.
//
// A function rather than an interface: there is one shape, and a test double is
// a closure. It exists so a deployment can verify Google's IAP JWT assertion, or
// an OIDC token, without this package taking on a JWT dependency that most
// deployments would not use. Roster ships none, exactly as petstore-reference
// ships none.
type TokenValidator func(ctx context.Context, token string) (string, error)

// Authenticator resolves a request's credentials to a subject.
type Authenticator struct {
	queries *db.Queries

	// trustProxy permits identity headers set by an upstream proxy. It must only
	// be on where clients cannot reach this service without passing through that
	// proxy, because on a directly reachable listener any caller could set
	// X-Forwarded-User and become anyone.
	trustProxy bool

	// validate, when set, verifies a presented credential rather than believing
	// the header that carried it. Optional; nil means header trust alone.
	validate TokenValidator
}

// AuthenticatorOption configures an Authenticator.
type AuthenticatorOption func(*Authenticator)

// WithTrustedProxyHeaders permits identity headers from an upstream proxy.
//
// Off unless asked for, because the safe default is the one that cannot be
// spoofed.
func WithTrustedProxyHeaders() AuthenticatorOption {
	return func(a *Authenticator) { a.trustProxy = true }
}

// WithTokenValidator verifies credentials with the given function.
//
// With a validator configured, an IAP request that carries no signed assertion
// is refused rather than falling back to trusting the plain headers. An operator
// who asked for verification gets it or gets an error, never a silent downgrade.
func WithTokenValidator(validate TokenValidator) AuthenticatorOption {
	return func(a *Authenticator) { a.validate = validate }
}

// NewAuthenticator builds an Authenticator over the given pool.
func NewAuthenticator(pool *pgxpool.Pool, opts ...AuthenticatorOption) *Authenticator {
	a := &Authenticator{}
	if pool != nil {
		a.queries = db.New(pool)
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// NewInterceptor returns the Connect interceptor that authenticates requests.
//
// Unary only: this service defines no streaming methods, so the streaming paths
// pass through to the next interceptor unchanged rather than pretending to
// handle a case that cannot occur.
func (a *Authenticator) NewInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			subject, err := a.authenticate(ctx, req.Header())
			if err != nil {
				return nil, err
			}
			return next(NewContextWithSubject(ctx, subject), req)
		}
	}
}

// authenticate resolves a request's credentials to a subject.
//
// Two ways in, tried in that order: an identity asserted by a trusted upstream
// proxy, or a bearer token this service issued. The proxy path never touches the
// database, and that is deliberate rather than an optimisation. Authentication
// answers who is calling; whether that subject may see a row is decided by each
// query's join against auth_effective_access, which filters NOT p.disabled in
// both of its branches. A subject with no principal, or a disabled one, therefore
// authenticates and then sees nothing and writes nothing.
//
// Returns a Connect error directly rather than a domain error: it runs before
// the handler, so there is no translate() boundary between it and the client.
func (a *Authenticator) authenticate(ctx context.Context, header http.Header) (string, error) {
	if id, ok := proxyIdentity(header, a.trustProxy); ok {
		// Only IAP forwards something signed. An oauth2-proxy identity is a plain
		// header and nothing more, so a validator has nothing to verify there and
		// the trust decision was already made by --trust-proxy-headers. Consulting
		// it anyway would reject every request behind an oauth2-proxy.
		if a.validate == nil || id.provider != providerIAP {
			return id.subject, nil
		}
		// Verification was asked for, so it happens or the request is refused.
		if id.assertion == "" {
			return "", connect.NewError(connect.CodeUnauthenticated, errUnsignedAssertion)
		}
		return a.verify(ctx, id.assertion)
	}

	token, err := bearerToken(header.Get("Authorization"))
	if err != nil {
		return "", connect.NewError(connect.CodeUnauthenticated, err)
	}

	// A validator takes precedence over the token table: a deployment that
	// verifies OIDC tokens has no rows to look them up in.
	if a.validate != nil {
		return a.verify(ctx, token)
	}

	if a.queries == nil {
		return "", connect.NewError(connect.CodeUnavailable, errNoDatabase)
	}

	// The database stores only the hash, so a dump of auth_api_token cannot be
	// replayed against this service.
	sum := sha256.Sum256([]byte(token))
	principal, err := a.queries.GetPrincipalByTokenHash(ctx, sum[:])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// One message for an unknown, revoked, expired, or disabled token:
			// distinguishing them would tell a prober which of those a guessed
			// token is.
			return "", connect.NewError(connect.CodeUnauthenticated, errBadCredential)
		}
		return "", connect.NewError(connect.CodeInternal, errInternal)
	}

	// Best-effort: a failed timestamp update must not fail the request it was
	// recording, and the next call will try again.
	_ = a.queries.TouchAPToken(ctx, sum[:])

	return principal.Subject, nil
}

// verify runs the configured validator and insists it named someone.
//
// A validator that returns no subject and no error is a bug in that validator,
// and letting the result through would authenticate the request as nobody. The
// empty subject matches no row in auth_effective_access, so nothing would leak,
// but the caller would be told "unauthenticated" by a handler far away from the
// validator that actually caused it.
func (a *Authenticator) verify(ctx context.Context, credential string) (string, error) {
	subject, err := a.validate(ctx, credential)
	if err != nil || strings.TrimSpace(subject) == "" {
		return "", connect.NewError(connect.CodeUnauthenticated, errBadCredential)
	}
	return subject, nil
}
