package paging

import (
	"encoding/base64"

	"github.com/ninja-cloudbuild/cloudbuild/server/util/status"
	"google.golang.org/protobuf/proto"

	pgpb "github.com/ninja-cloudbuild/cloudbuild/proto/pagination"
)

// EncodeOffsetLimit returns an opaque token representing the given OffsetLimit
// token.
func EncodeOffsetLimit(token *pgpb.OffsetLimit) (string, error) {
	data, err := proto.Marshal(token)
	if err != nil {
		return "", status.InternalErrorf("failed to marshal page token: %s", err)
	}
	str := base64.StdEncoding.EncodeToString(data)
	return str, nil
}

// DecodeOffsetLimit decodes a string that has been previously encoded via
// EncodeOffsetLimit.
func DecodeOffsetLimit(str string) (*pgpb.OffsetLimit, error) {
	data, err := base64.StdEncoding.DecodeString(str)
	if err != nil {
		return nil, status.InvalidArgumentErrorf("failed to decode page token %q: %s", str, err)
	}
	t := &pgpb.OffsetLimit{}
	if err := proto.Unmarshal(data, t); err != nil {
		return nil, status.InvalidArgumentErrorf("failed to unmarshal page token: %s", err)
	}
	return t, nil
}
