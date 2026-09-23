package store

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// cursorVersion lets a future change to the encoding reject old cursors
// cleanly instead of misreading them.
const cursorVersion = 1

// pageCursor is what an opaque cursor string decodes to.
//
// It carries a fingerprint of the filters it was produced under. Without one,
// a client that changes a filter and reuses its cursor gets a page computed
// against the old query - wrong results, no error, and very hard to notice.
// With it, the cursor is rejected and the client starts again from the top,
// which is what it wanted anyway.
type pageCursor struct {
	Version int               `json:"v"`
	Filter  string            `json:"f"`
	Key     map[string]string `json:"k"`
}

// encodeCursor turns a DynamoDB LastEvaluatedKey into an opaque string.
// Returns "" when there are no more pages, which is the signal callers use.
//
// Every key attribute in this table is a string, so the encoding does not
// need to carry types.
func encodeCursor(filterHash string, last map[string]types.AttributeValue) (string, error) {
	if len(last) == 0 {
		return "", nil
	}

	key := make(map[string]string, len(last))
	for name, av := range last {
		s, ok := av.(*types.AttributeValueMemberS)
		if !ok {
			return "", fmt.Errorf("cursor: key attribute %q is not a string", name)
		}
		key[name] = s.Value
	}

	raw, err := json.Marshal(pageCursor{Version: cursorVersion, Filter: filterHash, Key: key})
	if err != nil {
		return "", fmt.Errorf("cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// decodeCursor reverses encodeCursor, rejecting anything that wasn't produced
// for this exact set of filters.
func decodeCursor(encoded, filterHash string) (map[string]types.AttributeValue, error) {
	if encoded == "" {
		return nil, nil
	}

	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, ErrBadCursor
	}

	var c pageCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, ErrBadCursor
	}
	if c.Version != cursorVersion || len(c.Key) == 0 {
		return nil, ErrBadCursor
	}
	if c.Filter != filterHash {
		// The filters changed under the client's feet. Better a clear error
		// than a page of the wrong results.
		return nil, ErrBadCursor
	}

	key := make(map[string]types.AttributeValue, len(c.Key))
	for name, value := range c.Key {
		key[name] = &types.AttributeValueMemberS{Value: value}
	}
	return key, nil
}
