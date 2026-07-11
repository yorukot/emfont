package font

import (
	"context"
	"io"
	"time"

	domain "github.com/emfont/emfont/backend/internal/domain/font"
)

type Repository interface {
	GetFontFamily(context.Context, string) (domain.Family, error)
	ListFontFamilies(context.Context, string, string, int) ([]domain.Family, error)
	GetFontSource(context.Context, string, int) (domain.Source, error)
	GetFontArtifact(context.Context, string) (domain.Artifact, error)
	CreateFontArtifact(context.Context, domain.Artifact) error
	MarkFontArtifactReady(context.Context, string, domain.BuildClaim, domain.ArtifactObject) error
	MarkFontArtifactMissing(context.Context, string, string, int64, string) (bool, error)
	TouchFontArtifact(context.Context, string, string, int64, time.Duration) (bool, error)
	GetStaticPackSnapshot(context.Context, string, []string) ([]domain.StaticPackSnapshot, error)
	AcquireBuildJob(context.Context, string, string, time.Duration) (domain.BuildClaim, bool, error)
	BuildRetryAfter(context.Context, string) (time.Duration, error)
	FailBuildJob(context.Context, string, domain.BuildClaim, string) error
	FailBuildJobTerminal(context.Context, string, domain.BuildClaim, string, string) error
}

type ObjectStore interface {
	StatObject(context.Context, string, string) (ObjectInfo, error)
	OpenObject(context.Context, string, string) (io.ReadCloser, ObjectInfo, error)
	PutObject(context.Context, string, io.Reader, int64, PutObjectOptions) (ObjectInfo, error)
	PublicURL(context.Context, string, string) (string, error)
}

type Builder interface {
	BuildSubset(context.Context, BuildInput) (BuildOutput, error)
}

type ObjectInfo struct {
	Key              string
	VersionID        string
	ETag             string
	SizeBytes        int64
	ContentType      string
	ChecksumSHA256   string
	ChecksumVerified bool
	LastModified     time.Time
}

type PutObjectOptions struct {
	ContentType    string
	ChecksumSHA256 string
}

type BuildInput struct {
	Source       []byte
	Codepoints   []rune
	SourceFormat string
	TargetFormat string
}

type BuildOutput struct {
	Data           []byte
	ContentType    string
	Format         string
	GlyphCount     int
	BuilderVersion string
}
