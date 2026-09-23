package store

import (
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/jfortner8/archive-api/internal/store/dynamo"
)

func keyToAV(key dynamo.Key) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: key.PK},
		"sk": &types.AttributeValueMemberS{Value: key.SK},
	}
}

func numberAV(n int64) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: strconv.FormatInt(n, 10)}
}

func stringAttr(av map[string]types.AttributeValue, name string) (string, error) {
	raw, ok := av[name]
	if !ok {
		return "", fmt.Errorf("row is missing %q", name)
	}
	s, ok := raw.(*types.AttributeValueMemberS)
	if !ok {
		return "", fmt.Errorf("row attribute %q is not a string", name)
	}
	return s.Value, nil
}
