package oneroster

import (
	"net/http"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/fieldmaskpb"

	v1 "github.com/StevenACoffman/roster/gen/go/oneroster/v1p2/v1"
)

// Benchmarks for the write and authentication paths.
//
// mergeWrite is reflective, which is the deliberate trade recorded in AGENTS.md:
// one generic merge instead of six hand-written ones that could drift. Reflection
// is the kind of code whose cost drifts quietly as fields are added, so the
// decision is worth a number attached to it rather than an assumption.
//
// proxyIdentity and bearerToken run on every single request, authenticated or
// not, which makes them the hottest code in this package by call count even
// though each one is cheap.

func BenchmarkRequireMask(b *testing.B) {
	benchmarks := []struct {
		name  string
		paths []string
	}{
		{name: "one field", paths: []string{"name"}},
		{name: "several fields", paths: []string{"name", "identifier", "type", "status"}},
		// The refusal path is not an error case to a caller sending a bad mask
		// repeatedly, so it should not be the expensive one.
		{name: "refused", paths: []string{"date_last_modified"}},
	}

	write := &v1.OrgWrite{}
	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			mask := &fieldmaskpb.FieldMask{Paths: bm.paths}
			b.ReportAllocs()

			for b.Loop() {
				//nolint:errcheck // the refused case returns an error by design.
				requireMask(mask, write)
			}
		})
	}
}

// BenchmarkMergeWrite measures the reflective projection and mask application
// that every update RPC performs once.
//
// Org is the smallest entity and User the largest, so the pair brackets the
// real cost: the projection loop runs once per field on the record, so the
// difference between them is what field count actually buys.
var benchUsername = "ada"

func BenchmarkMergeWrite(b *testing.B) {
	storedOrg := toProtoOrg(benchOrgRow())
	incomingOrg := &v1.OrgWrite{SourcedId: storedOrg.GetSourcedId(), Name: "Renamed"}

	storedUser := &v1.User{
		SourcedId: "usr-1", Status: "active", GivenName: "Ada", FamilyName: "Lovelace",
		EnabledUser: true, Username: &benchUsername,
		Roles: []*v1.Role{{
			Org:  &v1.OrgGUIDRef{SourcedId: "sch-1", Href: "/orgs/sch-1", Type: "org"},
			Role: "student", RoleType: "primary",
		}},
	}
	incomingUser := &v1.UserWrite{SourcedId: "usr-1", GivenName: "Augusta"}

	b.Run("Org, 8 fields", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			if _, err := mergeWrite(storedOrg, incomingOrg, []string{"name"}); err != nil {
				b.Fatalf("mergeWrite: %v", err)
			}
		}
	})

	b.Run("User, 25 fields", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			if _, err := mergeWrite(storedUser, incomingUser, []string{"given_name"}); err != nil {
				b.Fatalf("mergeWrite: %v", err)
			}
		}
	})
}

// BenchmarkToStorageOrg covers the other half of a write: the proto payload
// flattened into the columns the guarded statement binds.
func BenchmarkToStorageOrg(b *testing.B) {
	write := &v1.OrgWrite{
		SourcedId: "sch-1", Status: "active", Name: "Example High School",
		Type: "school", Identifier: "EHS",
		Parent: &v1.OrgGUIDRef{SourcedId: "dist-1", Href: "/orgs/dist-1", Type: "org"},
	}
	now := time.Unix(1_700_000_000, 0).UTC()

	b.ReportAllocs()

	for b.Loop() {
		toStorageOrg(write, now)
	}
}

// BenchmarkProxyIdentity runs on every request before anything else, so its
// cost is paid whether or not proxy headers are in play.
func BenchmarkProxyIdentity(b *testing.B) {
	iap := http.Header{
		headerIAPID:        []string{iapPrefix + "118234567890123456789"},
		headerIAPEmail:     []string{iapPrefix + "ada@example.test"},
		headerIAPAssertion: []string{"eyJhbGciOiJFUzI1NiIsImtpZCI6ImFiYyJ9.eyJzdWIiOiIxMTgifQ.sig"},
	}
	forwarded := http.Header{headerForwardedUser: []string{"ada@example.test"}}
	none := http.Header{"Authorization": []string{"Bearer abc"}}

	for _, bm := range []struct {
		name       string
		header     http.Header
		trustProxy bool
	}{
		// The case every token-authenticated deployment pays: the flag is off,
		// so this must be as close to free as a function call gets.
		{name: "untrusted, returns immediately", header: iap, trustProxy: false},
		{name: "IAP", header: iap, trustProxy: true},
		{name: "oauth2-proxy", header: forwarded, trustProxy: true},
		{name: "trusted but no identity headers", header: none, trustProxy: true},
	} {
		b.Run(bm.name, func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				proxyIdentity(bm.header, bm.trustProxy)
			}
		})
	}
}

// BenchmarkBearerToken measures the constant-time scheme comparison every
// token-authenticated request performs.
func BenchmarkBearerToken(b *testing.B) {
	for _, bm := range []struct {
		name   string
		header string
	}{
		{name: "a token", header: "Bearer 7f3c1a9e4b2d8f60a1c5e9b3d7f2a4c6"},
		{name: "another scheme", header: "Basic dXNlcjpwYXNzd29yZA=="},
		{name: "absent", header: ""},
	} {
		b.Run(bm.name, func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				//nolint:errcheck // the refusal cases return an error by design.
				bearerToken(bm.header)
			}
		})
	}
}
