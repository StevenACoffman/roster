package oneroster

import (
	"testing"
	"time"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/StevenACoffman/roster/gen/go/oneroster/v1p2/v1"
)

// validOrg returns an Org that passes validation, so a test mutating one field
// measures that field's constraint rather than another required field's absence.
func validOrg() *v1.Org {
	return &v1.Org{
		SourcedId:        "sch-1",
		Identifier:       "S1",
		Name:             "Example School",
		Type:             "school",
		Status:           "active",
		DateLastModified: timestamppb.New(time.Unix(0, 0).UTC()),
	}
}

// The inbound half of the closed-vocabulary defence.
//
// status, role_type and the GUIDRef types are plain strings now, so nothing in
// the type system stops a caller sending "ORG_STATUS_ACTIVE" or "banana". What
// stops them is the `in:`/`const:` constraint on each proto field, enforced by
// the protovalidate interceptor before any handler runs. These tests assert
// those constraints are actually present and actually reject — a constraint
// that silently went missing from the proto would otherwise be invisible until
// bad data was already stored.

func TestOrgStatusIsValidatedInbound(t *testing.T) {
	t.Parallel()

	validator, err := protovalidate.New()
	if err != nil {
		t.Fatalf("protovalidate.New: %v", err)
	}

	tests := []struct {
		name    string
		status  string
		wantErr bool
	}{
		{name: "active is accepted", status: "active"},
		{name: "tobedeleted is accepted", status: "tobedeleted"},
		{name: "protobuf enum spelling is rejected", status: "ORG_STATUS_ACTIVE", wantErr: true},
		{name: "unknown value is rejected", status: "inactive", wantErr: true},
		{name: "wrong case is rejected", status: "Active", wantErr: true},
		{name: "empty is rejected", status: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			org := validOrg()
			org.Status = tt.status
			err := validator.Validate(org)
			if tt.wantErr && err == nil {
				t.Errorf("status %q was accepted; the `in:` constraint is not enforcing", tt.status)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("status %q was rejected: %v", tt.status, err)
			}
		})
	}
}

func TestGUIDRefTypeIsValidatedInbound(t *testing.T) {
	t.Parallel()

	validator, err := protovalidate.New()
	if err != nil {
		t.Fatalf("protovalidate.New: %v", err)
	}

	tests := []struct {
		name    string
		refType string
		wantErr bool
	}{
		{name: "org is accepted", refType: "org"},
		{name: "protobuf enum spelling is rejected", refType: "ORG_GUID_REF_TYPE_ORG", wantErr: true},
		{name: "another entity's type is rejected", refType: "class", wantErr: true},
		{name: "empty is rejected", refType: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ref := &v1.OrgGUIDRef{
				Href:      "/ims/oneroster/rostering/v1p2/orgs/sch-1",
				SourcedId: "sch-1",
				Type:      tt.refType,
			}
			err := validator.Validate(ref)
			if tt.wantErr && err == nil {
				t.Errorf("type %q was accepted; the `const:` constraint is not enforcing", tt.refType)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("type %q was rejected: %v", tt.refType, err)
			}
		})
	}
}

func TestRoleTypeIsValidatedInbound(t *testing.T) {
	t.Parallel()

	validator, err := protovalidate.New()
	if err != nil {
		t.Fatalf("protovalidate.New: %v", err)
	}

	tests := []struct {
		name     string
		roleType string
		wantErr  bool
	}{
		{name: "primary is accepted", roleType: "primary"},
		{name: "secondary is accepted", roleType: "secondary"},
		{name: "enum spelling is rejected", roleType: "ROLE_ROLE_TYPE_PRIMARY", wantErr: true},
		{name: "empty is rejected", roleType: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			role := &v1.Role{
				Role:     "teacher",
				RoleType: tt.roleType,
				Org: &v1.OrgGUIDRef{
					Href:      "/ims/oneroster/rostering/v1p2/orgs/sch-1",
					SourcedId: "sch-1",
					Type:      "org",
				},
			}
			err := validator.Validate(role)
			if tt.wantErr && err == nil {
				t.Errorf("role_type %q was accepted; the `in:` constraint is not enforcing", tt.roleType)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("role_type %q was rejected: %v", tt.roleType, err)
			}
		})
	}
}

// TestInboundAndOutboundVocabulariesAgree pins the two halves together: a value
// protovalidate accepts must survive the outbound check, and one it rejects must
// not. A drift between them would mean the service accepts a value it then
// refuses to serve back.
func TestInboundAndOutboundVocabulariesAgree(t *testing.T) {
	t.Parallel()

	validator, err := protovalidate.New()
	if err != nil {
		t.Fatalf("protovalidate.New: %v", err)
	}

	for _, candidate := range []string{
		"active", "tobedeleted", "inactive", "ORG_STATUS_ACTIVE", "Active", "",
	} {
		org := validOrg()
		org.Status = candidate
		inboundOK := validator.Validate(org) == nil
		outboundOK := checkedStatus(candidate) != ""

		if inboundOK != outboundOK {
			t.Errorf("status %q: protovalidate accepts=%v but checkedStatus keeps=%v",
				candidate, inboundOK, outboundOK)
		}
	}
}
