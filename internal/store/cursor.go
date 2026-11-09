package store

import (
	"encoding/base64"
	"encoding/json"
)

// cursorVersion lets a future change to the encoding reject old cursors
// cleanly rather than misread them.
const cursorVersion = 2

// pageCursor is what an opaque cursor string decodes to.
//
// It carries a fingerprint of the filters it was produced under. Without one,
// a client that changes a filter and reuses its cursor is served a page
// computed against the old query - wrong results, no error, and very hard to
// notice. With it the cursor is rejected and the client starts from the top,
// which is what it wanted anyway.
//
// After is the index sort key of the last card on the previous page, so
// paging is keyset rather than offset and does not shift under an insert.
type pageCursor struct {
	Version int    `json:"v"`
	Filter  string `json:"f"`
	After   string `json:"a"`
}

func encodeCursor(filterHash, after string) (string, error) {
	if after == "" {
		return "", nil
	}

	raw, err := json.Marshal(pageCursor{Version: cursorVersion, Filter: filterHash, After: after})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// decodeCursor reverses encodeCursor, rejecting anything not produced for
// this exact set of filters.
func decodeCursor(encoded, filterHash string) (string, error) {
	if encoded == "" {
		return "", nil
	}

	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", ErrBadCursor
	}

	var c pageCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", ErrBadCursor
	}
	if c.Version != cursorVersion || c.After == "" {
		return "", ErrBadCursor
	}
	if c.Filter != filterHash {
		// The filters changed under the client's feet. Better a clear error
		// than a page of the wrong results.
		return "", ErrBadCursor
	}

	return c.After, nil
}
