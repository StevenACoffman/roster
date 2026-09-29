package oneroster

import "context"

// The authenticated subject travels on the context because it is request-scoped
// and every query needs it — §6's "transport layer sets the user on the context
// after authenticating".
//
// It carries only the subject string, not a resolved set of permissions. The
// permissions live in the database and are applied by each query's join against
// auth_effective_access, so a grant revoked mid-session takes effect on the next
// request rather than persisting in a cached context value.

// contextKey is unexported and of a named type, so no other package can collide
// with this key or read the value without going through the accessors below.
type contextKey int

const subjectContextKey contextKey = iota

// NewContextWithSubject returns ctx carrying the authenticated subject.
//
// Called by the authentication interceptor once per request, after the
// credential has been verified. Nothing else should call it in production; tests
// call it to drive the handler without an interceptor.
func NewContextWithSubject(ctx context.Context, subject string) context.Context {
	return context.WithValue(ctx, subjectContextKey, subject)
}

// SubjectFromContext reports the authenticated subject and whether one is
// present.
//
// An absent subject is the unauthenticated case. It is never treated as a
// wildcard: the queries filter on the subject, so an empty one matches no row in
// auth_effective_access and therefore sees nothing. The handler still rejects it
// explicitly, because returning an empty list would tell a caller with no
// credential that the roster is empty rather than that they are anonymous.
func SubjectFromContext(ctx context.Context) (string, bool) {
	subject, ok := ctx.Value(subjectContextKey).(string)
	if !ok || subject == "" {
		return "", false
	}
	return subject, true
}
