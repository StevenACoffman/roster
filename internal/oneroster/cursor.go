package oneroster

import (
	"encoding/base64"
	"fmt"
)

// Cursor tokens are opaque to clients by construction, not by obscurity: the
// encoding is public, but base64 signals "do not parse this" and leaves room to
// change the keyset without changing the wire contract.
//
// The keyset is a single column — sourced_id, the primary key of every rostering
// table — so the token holds exactly that value. A compound key would need a
// separator and an escaping rule here; one column needs neither.

// encodeCursor produces the page token that resumes iteration after sourcedID.
func encodeCursor(sourcedID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(sourcedID))
}

// decodeCursor reverses encodeCursor.
//
// An empty token is not an error — it means "start at the beginning", and the
// empty string it returns sorts before every non-empty identifier, so the same
// `sourced_id > $cursor` predicate serves the first page and every later one.
//
// A malformed token wraps errInvalid, so a caller who pastes junk gets
// InvalidArgument rather than an internal error or, worse, a silent restart from
// the beginning of the collection.
func decodeCursor(token string) (string, error) {
	if token == "" {
		return "", nil
	}

	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", fmt.Errorf("%w: page_token is not valid base64url", errInvalid)
	}

	return string(raw), nil
}
