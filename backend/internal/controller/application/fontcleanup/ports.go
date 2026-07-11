package fontcleanup

import (
	"context"
	"time"
)

type Repository interface {
	RetireFontArtifacts(context.Context, time.Time, time.Time, int32) (int64, error)
	DeleteRetiredFontArtifacts(context.Context, time.Time, int32) (int64, error)
	FindReferencedFontObjectKeys(context.Context, []string) ([]string, error)
}

type ObjectStore interface {
	ListObjects(context.Context, string, string, int) (ObjectPage, error)
	DeleteObject(context.Context, Object) error
}

type Object struct {
	Key          string    `json:"key"`
	ETag         string    `json:"etag,omitempty"`
	SizeBytes    int64     `json:"sizeBytes,omitempty"`
	LastModified time.Time `json:"lastModified"`
}

// ObjectPage is ordered lexically by object key. NextCursor must be non-empty
// when HasMore is true and must resume strictly after every object in Objects.
type ObjectPage struct {
	Objects    []Object `json:"objects"`
	NextCursor string   `json:"nextCursor,omitempty"`
	HasMore    bool     `json:"hasMore"`
}
