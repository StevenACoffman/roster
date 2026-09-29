package oneroster

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
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

// devSubjectHeader names the caller when token authentication is disabled. It is
// read only in that mode, so a request cannot use it to override a real
// credential.
const devSubjectHeader = "X-Roster-Dev-Subject"

var (
	errMissingCredential = errors.New("missing bearer token")
	errBadCredential     = errors.New("bearer token is not recognized")
)

// Authenticator resolves bearer tokens to subjects.
type Authenticator struct {
	queries *db.Queries

	// devSubject, when set, authenticates every request as this subject without a
	// token. It exists so a local checkout runs without provisioning credentials,
	// and the serve command refuses to enable it outside development.
	devSubject string
}

// NewAuthenticator builds an Authenticator over the given pool.
//
// A non-empty devSubject disables token checking entirely; the caller is
// responsible for allowing that only in development.
func NewAuthenticator(pool *pgxpool.Pool, devSubject string) *Authenticator {
	a := &Authenticator{devSubject: devSubject}
	if pool != nil {
		a.queries = db.New(pool)
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
			subject, err := a.authenticate(ctx, req.Header().Get("Authorization"),
				req.Header().Get(devSubjectHeader))
			if err != nil {
				return nil, err
			}
			return next(NewContextWithSubject(ctx, subject), req)
		}
	}
}

// authenticate resolves a request's credential to a subject.
//
// Returns a Connect error directly rather than a domain error: it runs before
// the handler, so there is no translate() boundary between it and the client.
func (a *Authenticator) authenticate(ctx context.Context, authorization, devHeader string) (string, error) {
	if a.devSubject != "" {
		// A header may name a different subject in development, which is what
		// makes it possible to exercise scoping locally without minting tokens.
		if devHeader != "" {
			return devHeader, nil
		}
		return a.devSubject, nil
	}

	token, err := bearerToken(authorization)
	if err != nil {
		return "", connect.NewError(connect.CodeUnauthenticated, err)
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
