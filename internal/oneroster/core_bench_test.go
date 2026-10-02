package oneroster

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	v1 "github.com/StevenACoffman/roster/gen/go/oneroster/v1p2/v1"
	"github.com/StevenACoffman/roster/internal/db"
)

// Benchmarks for the conversion layer, which is the code every read RPC runs
// once per row returned.
//
// Allocations are the measurement that matters here, not wall time. A page of
// 100 orgs runs toProtoOrg 100 times, and a change that adds one allocation per
// row adds a hundred per request before anything is serialised. That cost is
// invisible in a test and invisible in a profile taken under light load, which
// is why it is gated on a pull request instead.
//
// Each benchmark reports b.ReportAllocs so the gate has B/op and allocs/op to
// compare; the CI job gates on those two and reports ns/op without blocking.

// benchOrgRow is the shape a scoped read actually returns: every nullable column
// populated, because a row with NULL parents skips orgRef entirely and would
// measure the cheap path rather than the usual one.
func benchOrgRow() db.Org {
	return db.Org{
		SourcedID:        "sch-00001",
		Status:           "active",
		DateLastModified: pgtype.Timestamptz{Time: time.Unix(1_700_000_000, 0).UTC(), Valid: true},
		Metadata:         []byte(`{"district":"north","sis_id":"SCH-1","wave":2}`),
		Name:             "Example High School",
		Type:             "school",
		Identifier:       "EHS",
		ParentSourcedID:  pgtype.Text{String: "dist-1", Valid: true},
		ParentHref:       pgtype.Text{Valid: false},
	}
}

func BenchmarkToProtoOrg(b *testing.B) {
	row := benchOrgRow()
	b.ReportAllocs()

	for b.Loop() {
		toProtoOrg(row)
	}
}

// BenchmarkToProtoOrgPage measures the unit a client actually receives. The
// per-row benchmark above can hide a cost that only shows up at page scale, and
// this is the number that moves when a conversion starts allocating.
// benchOrgPage keeps the converted page reachable; see the note in the loop.
var benchOrgPage []*v1.Org

func BenchmarkToProtoOrgPage(b *testing.B) {
	const pageSize = 100

	rows := make([]db.Org, pageSize)
	for i := range rows {
		rows[i] = benchOrgRow()
	}

	b.ReportAllocs()

	for b.Loop() {
		page := make([]*v1.Org, 0, len(rows))
		// Indexed rather than ranged by value: db.Org is 184 bytes, and copying
		// it per iteration would put the struct copy in the measurement next to
		// the conversion being measured.
		for i := range rows {
			page = append(page, toProtoOrg(rows[i]))
		}
		// A typed slice assignment, which is three words and no allocation, so
		// the page cannot be optimised away without distorting allocs/op.
		benchOrgPage = page
	}
}

// BenchmarkMetadata covers the JSONB extension bag, which is decoded once per
// row and is the only part of a conversion that parses anything.
func BenchmarkMetadata(b *testing.B) {
	benchmarks := []struct {
		name string
		raw  []byte
	}{
		// The common case by a wide margin: most rows carry no extensions, and
		// this path must stay free.
		{name: "absent", raw: nil},
		{name: "small", raw: []byte(`{"sis_id":"SCH-1"}`)},
		{name: "typical", raw: []byte(`{"district":"north","sis_id":"SCH-1","wave":2,"tags":["a","b"]}`)},
		// Dropped rather than failing the read, so the error path runs per row
		// too and is worth measuring.
		{name: "malformed", raw: []byte(`{"district":`)},
	}

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				metadata(bm.raw)
			}
		})
	}
}

// BenchmarkRefHref covers both halves of the href decision, because the
// synthesised path concatenates and the stored path does not.
func BenchmarkRefHref(b *testing.B) {
	benchmarks := []struct {
		name string
		href pgtype.Text
	}{
		{name: "stored", href: pgtype.Text{String: "/ims/oneroster/rostering/v1p2/orgs/sch-1", Valid: true}},
		{name: "synthesized", href: pgtype.Text{}},
	}

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				refHref(bm.href, collectionOrgs, "sch-1")
			}
		})
	}
}

// BenchmarkCheckedVocabulary is the outbound half of the closed-vocabulary
// defence, run once per enum-shaped field on every row. A linear scan over a
// short list is the right implementation, and this is what would notice it being
// replaced by something that allocates.
func BenchmarkCheckedVocabulary(b *testing.B) {
	benchmarks := []struct {
		name  string
		value string
	}{
		{name: "first", value: "active"},
		{name: "last", value: "tobedeleted"},
		{name: "rejected", value: "ORG_STATUS_ACTIVE"},
	}

	for _, bm := range benchmarks {
		b.Run(bm.name, func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				checkedStatus(bm.value)
			}
		})
	}
}

// BenchmarkCursorRoundTrip measures the paging codec, which runs twice per
// paged request: once to decode the incoming token and once to encode the next.
func BenchmarkCursorRoundTrip(b *testing.B) {
	const id = "sch-0000000042"

	b.ReportAllocs()

	for b.Loop() {
		if _, err := decodeCursor(encodeCursor(id)); err != nil {
			b.Fatalf("decodeCursor: %v", err)
		}
	}
}

// BenchmarkPaginate covers the slice-and-token step at the end of every paged
// read. The full page is the case that encodes a token; a short page returns
// early and does not.
func BenchmarkPaginate(b *testing.B) {
	const limit = 51

	full := make([]db.Org, limit)
	short := make([]db.Org, limit-10)
	for i := range full {
		full[i] = benchOrgRow()
	}
	for i := range short {
		short[i] = benchOrgRow()
	}

	id := func(row db.Org) string { return row.SourcedID }

	for _, bm := range []struct {
		name string
		rows []db.Org
	}{
		{name: "full page encodes a token", rows: full},
		{name: "short page ends iteration", rows: short},
	} {
		b.Run(bm.name, func(b *testing.B) {
			b.ReportAllocs()

			for b.Loop() {
				paginate(bm.rows, limit, id)
			}
		})
	}
}
