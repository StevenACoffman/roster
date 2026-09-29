package oneroster

import (
	"context"
	"testing"
)

func TestSubjectFromContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		ctx         func() context.Context
		wantSubject string
		wantOK      bool
	}{
		{
			name:   "absent subject is the anonymous case",
			ctx:    context.Background,
			wantOK: false,
		},
		{
			name: "empty subject is anonymous, not a wildcard",
			ctx: func() context.Context {
				return NewContextWithSubject(context.Background(), "")
			},
			wantOK: false,
		},
		{
			name: "subject round-trips",
			ctx: func() context.Context {
				return NewContextWithSubject(context.Background(), "oidc|ana")
			},
			wantSubject: "oidc|ana",
			wantOK:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			subject, ok := SubjectFromContext(tt.ctx())
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			if subject != tt.wantSubject {
				t.Errorf("subject = %q, want %q", subject, tt.wantSubject)
			}
		})
	}
}

// foreignKey stands in for another package's context key. Its underlying type
// and value are identical to this package's subjectContextKey, which is exactly
// the collision an unexported named key type has to rule out: context values
// compare on dynamic type as well as value, so these must not match.
type foreignKey int

const foreignSubjectKey foreignKey = 0

// TestForeignContextKeyDoesNotCollide guards the property that another package
// cannot inject an authenticated subject by writing to a key that looks the
// same. A regression here — switching subjectContextKey to a bare string, say —
// would let any package in the binary forge an identity.
func TestForeignContextKeyDoesNotCollide(t *testing.T) {
	t.Parallel()

	ctx := context.WithValue(context.Background(), foreignSubjectKey, "attacker")
	if subject, ok := SubjectFromContext(ctx); ok {
		t.Errorf("a foreign key was read as an authenticated subject %q", subject)
	}
}
