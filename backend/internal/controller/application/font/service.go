package font

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	domain "github.com/emfont/emfont/backend/internal/domain/font"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

const defaultMaxSourceBytes = int64(128 << 20)

type Config struct {
	BuilderVersion         string
	BuildLease             time.Duration
	BuildTimeout           time.Duration
	StaticBuildConcurrency int
	WorkerID               string
	ForceMin               bool
	MaxSourceBytes         int64
}

type Service struct {
	repository Repository
	objects    ObjectStore
	builder    Builder
	config     Config
	builds     singleflight.Group
	buildSlots chan struct{}
}

func NewService(repository Repository, objects ObjectStore, builder Builder, cfg Config) (*Service, error) {
	if repository == nil {
		return nil, fmt.Errorf("%w: repository is required", ErrInvalidInput)
	}
	if objects == nil {
		return nil, fmt.Errorf("%w: object store is required", ErrInvalidInput)
	}
	if builder == nil {
		return nil, fmt.Errorf("%w: builder is required", ErrInvalidInput)
	}
	if cfg.BuilderVersion == "" {
		cfg.BuilderVersion = domain.DefaultBuilderVersion
	}
	if versioned, ok := builder.(interface{ Version() string }); ok && strings.TrimSpace(versioned.Version()) != "" {
		cfg.BuilderVersion += "+" + strings.TrimSpace(versioned.Version())
	}
	if cfg.BuildLease <= 0 {
		cfg.BuildLease = 2 * time.Minute
	}
	if cfg.BuildTimeout <= 0 {
		cfg.BuildTimeout = 90 * time.Second
	}
	if cfg.StaticBuildConcurrency <= 0 {
		cfg.StaticBuildConcurrency = 2
	}
	if cfg.MaxSourceBytes <= 0 {
		cfg.MaxSourceBytes = defaultMaxSourceBytes
	}
	if cfg.WorkerID == "" {
		hostname, _ := os.Hostname()
		cfg.WorkerID = hostname + "-" + strconv.Itoa(os.Getpid())
	}
	return &Service{
		repository: repository,
		objects:    objects,
		builder:    builder,
		config:     cfg,
		buildSlots: make(chan struct{}, cfg.StaticBuildConcurrency),
	}, nil
}

func (s *Service) Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error) {
	family, weight, normalized, codepoints, err := s.resolveRequest(ctx, req)
	if err != nil {
		return GenerateResponse{}, err
	}

	if req.Min || s.config.ForceMin {
		location, err := s.generateDynamic(ctx, family, weight, normalized, codepoints)
		if err != nil {
			return GenerateResponse{}, err
		}
		return successResponse(family, weight, domain.BuildModeDynamic, []string{location}), nil
	}

	locations, err := s.generateStatic(ctx, family, weight, normalized)
	if err != nil {
		return GenerateResponse{}, err
	}
	return successResponse(family, weight, domain.BuildModeStatic, locations), nil
}

func (s *Service) List(ctx context.Context, search string) ([]FontListItem, error) {
	families, err := s.repository.ListFontFamilies(normalizeContext(ctx), strings.TrimSpace(search))
	if err != nil {
		return nil, fmt.Errorf("list font families: %w", err)
	}
	result := make([]FontListItem, 0, len(families))
	for _, family := range families {
		var author *string
		if len(family.Authors) > 0 {
			value := family.Authors[0]
			author = &value
		}
		result = append(result, FontListItem{
			ID: family.ID, Name: family.Name, Weight: cloneInts(family.Weights), Author: author,
			NameZH: family.NameZH, NameEN: family.NameEN, Category: family.Category,
			Tags: cloneStrings(family.Tags), Family: family.Family, SID: family.DemoContentID,
		})
	}
	return result, nil
}

func (s *Service) Info(ctx context.Context, fontID string) (FontInfoDTO, error) {
	id, err := domain.NormalizeID(fontID)
	if err != nil {
		return FontInfoDTO{}, invalidInput(err)
	}
	family, err := s.repository.GetFontFamily(normalizeContext(ctx), id)
	if err != nil {
		return FontInfoDTO{}, mapRepositoryError("get font family", err)
	}
	var author *string
	if len(family.Authors) > 0 {
		value := family.Authors[0]
		author = &value
	}
	return FontInfoDTO{
		Name:     FontInfoName{Original: family.Name, ZH: family.NameZH, EN: family.NameEN},
		Category: family.Category, Weight: cloneInts(family.Weights), Tag: cloneStrings(family.Tags),
		Family: family.Family, Version: family.Version, License: family.License, Source: family.RepoURL,
		Author: author, Description: family.Description, Format: family.Format, SID: family.DemoContentID,
	}, nil
}

func (s *Service) CSS(ctx context.Context, req GenerateRequest) (string, error) {
	if strings.TrimSpace(req.Words) != "" {
		response, err := s.Generate(ctx, req)
		if err != nil {
			return "", err
		}
		sources := make([]string, 0, len(response.Location))
		for _, location := range response.Location {
			sources = append(sources, fmt.Sprintf("url('%s') format('woff2')", location))
		}
		return fmt.Sprintf("@font-face {\n  font-family: '%s';\n  src: %s;\n  font-weight: %d;\n  font-display: swap;\n}\n",
			response.Name, strings.Join(sources, ",\n       "), response.Weight,
		), nil
	}

	id, err := domain.NormalizeID(req.FontID)
	if err != nil {
		return "", invalidInput(err)
	}
	family, err := s.repository.GetFontFamily(normalizeContext(ctx), id)
	if err != nil {
		return "", mapRepositoryError("get font family", err)
	}
	weights := cloneInts(family.Weights)
	if strings.TrimSpace(req.Weight) != "" {
		requested, _, parseErr := domain.NormalizeWeight(req.Weight)
		if parseErr != nil {
			return "", invalidInput(parseErr)
		}
		resolved, resolveErr := domain.ResolveWeight(weights, requested)
		if resolveErr != nil {
			return "", ErrFontSourceNotFound
		}
		weights = []int{resolved}
	}
	if len(weights) == 0 {
		return "", ErrFontSourceNotFound
	}

	blocks := make([]string, 0, len(weights))
	for _, weight := range weights {
		source, sourceErr := s.fontSource(ctx, family, weight)
		if sourceErr != nil {
			return "", sourceErr
		}
		location, locationErr := s.objects.PublicURL(ctx, source.ObjectKey)
		if locationErr != nil {
			return "", fmt.Errorf("%w: resolve source URL: %v", ErrObjectStorageUnavailable, locationErr)
		}
		blocks = append(blocks, fmt.Sprintf("@font-face {\n  font-family: '%s';\n  src: url('%s') format('%s');\n  font-weight: %d;\n  font-display: swap;\n}\n",
			family.ID, location, cssFormat(source.Format), weight,
		))
	}
	return strings.Join(blocks, "\n"), nil
}

func (s *Service) resolveRequest(ctx context.Context, req GenerateRequest) (domain.Family, int, string, []rune, error) {
	id, err := domain.NormalizeID(req.FontID)
	if err != nil {
		return domain.Family{}, 0, "", nil, invalidInput(err)
	}
	family, err := s.repository.GetFontFamily(normalizeContext(ctx), id)
	if err != nil {
		return domain.Family{}, 0, "", nil, mapRepositoryError("get font family", err)
	}
	requested, _, err := domain.NormalizeWeight(req.Weight)
	if err != nil {
		return domain.Family{}, 0, "", nil, invalidInput(err)
	}
	weight, err := domain.ResolveWeight(family.Weights, requested)
	if err != nil {
		return domain.Family{}, 0, "", nil, fmt.Errorf("%w: no weight is available", ErrFontSourceNotFound)
	}
	normalized, codepoints, err := domain.NormalizeWordSet(req.Words)
	if err != nil {
		return domain.Family{}, 0, "", nil, invalidInput(err)
	}
	return family, weight, normalized, codepoints, nil
}

func (s *Service) generateDynamic(ctx context.Context, family domain.Family, weight int, normalized string, codepoints []rune) (string, error) {
	source, err := s.fontSource(ctx, family, weight)
	if err != nil {
		return "", err
	}
	wordHash := domain.DynamicWordHash(family.ID, weight, normalized)
	sourceFingerprint := domain.SourceFingerprint(source)
	revision := domain.BuildRevision(sourceFingerprint, s.config.BuilderVersion)
	artifact := domain.Artifact{
		Key:  domain.DynamicArtifactKey(wordHash, family.ID, weight, sourceFingerprint, s.config.BuilderVersion),
		Kind: domain.BuildModeDynamic, Status: "pending", FamilyID: family.ID, Weight: weight,
		WordHash: wordHash, NormalizedWordSet: normalized, SourceChecksum: sourceFingerprint,
		BuilderVersion: s.config.BuilderVersion, ObjectKey: domain.DynamicObjectKey(wordHash, family.ID, weight, revision),
		ContentType: domain.ContentTypeWOFF2,
	}
	return s.ensureArtifact(ctx, source, artifact, codepoints)
}

func (s *Service) generateStatic(ctx context.Context, family domain.Family, weight int, normalized string) ([]string, error) {
	characters := make([]string, 0, len([]rune(normalized)))
	for _, r := range normalized {
		characters = append(characters, string(r))
	}
	packs, err := s.repository.FindStaticPacks(ctx, family.ID, characters)
	if err != nil {
		return nil, fmt.Errorf("find static packs: %w", err)
	}
	if len(packs) == 0 {
		return []string{}, nil
	}
	version, err := s.repository.CurrentStaticVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("get static font version: %w", err)
	}
	source, err := s.fontSource(ctx, family, weight)
	if err != nil {
		return nil, err
	}
	sourceFingerprint := domain.SourceFingerprint(source)
	revision := domain.BuildRevision(sourceFingerprint, s.config.BuilderVersion)

	locations := make([]string, len(packs))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(s.config.StaticBuildConcurrency)
	for index, packNumber := range packs {
		index, packNumber := index, packNumber
		group.Go(func() error {
			text, err := s.repository.GetStaticPackCharacters(groupCtx, family.ID, packNumber)
			if err != nil {
				return fmt.Errorf("get static pack %d characters: %w", packNumber, err)
			}
			_, codepoints, err := domain.NormalizeWordSet(text)
			if err != nil {
				return fmt.Errorf("normalize static pack %d: %w", packNumber, err)
			}
			pack := domain.PackID(packNumber)
			artifact := domain.Artifact{
				Key:  domain.StaticArtifactKey(version, family.ID, weight, pack, sourceFingerprint, s.config.BuilderVersion),
				Kind: domain.BuildModeStatic, Status: "pending", FamilyID: family.ID, Weight: weight,
				Version: version, Pack: pack, SourceChecksum: sourceFingerprint,
				BuilderVersion: s.config.BuilderVersion, ObjectKey: domain.StaticObjectKey(version, family.ID, weight, pack, revision),
				ContentType: domain.ContentTypeWOFF2,
			}
			location, err := s.ensureArtifact(groupCtx, source, artifact, codepoints)
			if err != nil {
				return err
			}
			locations[index] = location
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	return locations, nil
}

func (s *Service) ensureArtifact(ctx context.Context, source domain.Source, artifact domain.Artifact, codepoints []rune) (string, error) {
	if location, ready, err := s.readyArtifact(ctx, artifact.Key); err != nil {
		return "", err
	} else if ready {
		return location, nil
	}
	if err := s.repository.CreateFontArtifact(ctx, artifact); err != nil {
		return "", fmt.Errorf("create font artifact: %w", err)
	}

	result := s.builds.DoChan(artifact.Key, func() (any, error) {
		buildRoot := context.WithoutCancel(ctx)
		if location, ready, readyErr := s.readyArtifact(buildRoot, artifact.Key); readyErr != nil {
			return "", readyErr
		} else if ready {
			return location, nil
		}
		return s.buildArtifact(buildRoot, source, artifact, codepoints)
	})
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case completed := <-result:
		if completed.Err != nil {
			return "", completed.Err
		}
		location, ok := completed.Val.(string)
		if !ok || location == "" {
			return "", fmt.Errorf("%w: artifact location is empty", ErrBuildFailed)
		}
		return location, nil
	}
}

func (s *Service) readyArtifact(ctx context.Context, key string) (string, bool, error) {
	artifact, err := s.repository.GetFontArtifact(ctx, key)
	if err != nil {
		if errors.Is(err, ErrArtifactNotFound) || errors.Is(err, domain.ErrArtifactNotFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("get font artifact: %w", err)
	}
	if artifact.Status != "ready" {
		return "", false, nil
	}
	info, err := s.objects.StatObject(ctx, artifact.ObjectKey)
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			_ = s.repository.MarkFontArtifactMissing(ctx, key, "object missing from storage")
			return "", false, nil
		}
		return "", false, fmt.Errorf("%w: stat artifact: %v", ErrObjectStorageUnavailable, err)
	}
	if info.SizeBytes <= 0 || artifact.SizeBytes > 0 && info.SizeBytes != artifact.SizeBytes {
		_ = s.repository.MarkFontArtifactMissing(ctx, key, "object size does not match artifact metadata")
		return "", false, nil
	}
	if artifact.ETag != "" && info.ETag != "" && normalizeETag(artifact.ETag) != normalizeETag(info.ETag) {
		_ = s.repository.MarkFontArtifactMissing(ctx, key, "object ETag does not match artifact metadata")
		return "", false, nil
	}
	if artifact.ChecksumSHA256 != "" && info.ChecksumSHA256 != "" &&
		!strings.EqualFold(normalizeChecksum(artifact.ChecksumSHA256), normalizeChecksum(info.ChecksumSHA256)) {
		_ = s.repository.MarkFontArtifactMissing(ctx, key, "object checksum does not match artifact metadata")
		return "", false, nil
	}
	location, err := s.objects.PublicURL(ctx, artifact.ObjectKey)
	if err != nil {
		return "", false, fmt.Errorf("%w: resolve artifact URL: %v", ErrObjectStorageUnavailable, err)
	}
	return location, true, nil
}

func (s *Service) buildArtifact(ctx context.Context, source domain.Source, artifact domain.Artifact, codepoints []rune) (location string, err error) {
	leaseOwner := newLeaseOwner(s.config.WorkerID)
	acquired, err := s.repository.AcquireBuildJob(ctx, artifact.Key, leaseOwner, s.config.BuildLease)
	if err != nil {
		return "", fmt.Errorf("acquire build job: %w", err)
	}
	if !acquired {
		return "", ErrBuildNotReady
	}

	buildCtx, cancel := context.WithTimeout(ctx, s.config.BuildTimeout)
	defer cancel()
	artifactReady := false
	defer func() {
		if err == nil {
			return
		}
		message := truncateError(err)
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cleanupCancel()
		if !artifactReady {
			_ = s.repository.MarkFontArtifactFailed(cleanupCtx, artifact.Key, leaseOwner, message)
		}
		_ = s.repository.FailBuildJob(cleanupCtx, artifact.Key, leaseOwner, message)
	}()
	select {
	case s.buildSlots <- struct{}{}:
		defer func() { <-s.buildSlots }()
	case <-buildCtx.Done():
		return "", buildCtx.Err()
	}

	reader, sourceInfo, err := s.objects.OpenObject(buildCtx, source.ObjectKey)
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			return "", fmt.Errorf("%w: %s", ErrFontSourceNotFound, source.ObjectKey)
		}
		return "", fmt.Errorf("%w: open font source: %v", ErrObjectStorageUnavailable, err)
	}
	defer reader.Close()
	if sourceInfo.SizeBytes > s.config.MaxSourceBytes {
		return "", fmt.Errorf("%w: source font exceeds %d bytes", ErrBuildFailed, s.config.MaxSourceBytes)
	}
	sourceBytes, err := io.ReadAll(io.LimitReader(reader, s.config.MaxSourceBytes+1))
	if err != nil {
		return "", fmt.Errorf("read font source: %w", err)
	}
	if int64(len(sourceBytes)) > s.config.MaxSourceBytes {
		return "", fmt.Errorf("%w: source font exceeds %d bytes", ErrBuildFailed, s.config.MaxSourceBytes)
	}
	if source.ChecksumSHA256 != "" {
		sourceChecksum := sha256.Sum256(sourceBytes)
		if !strings.EqualFold(normalizeChecksum(source.ChecksumSHA256), hex.EncodeToString(sourceChecksum[:])) {
			return "", fmt.Errorf("%w: source font checksum mismatch", ErrBuildFailed)
		}
	}

	output, err := s.builder.BuildSubset(buildCtx, BuildInput{
		Source: sourceBytes, Codepoints: codepoints, SourceFormat: source.Format, TargetFormat: domain.OutputFormatWOFF2,
	})
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrBuildFailed, err)
	}
	if len(output.Data) == 0 || output.GlyphCount == 0 {
		return "", fmt.Errorf("%w: subset contains no glyphs", ErrBuildFailed)
	}
	checksum := sha256.Sum256(output.Data)
	checksumHex := hex.EncodeToString(checksum[:])
	info, err := s.objects.PutObject(buildCtx, artifact.ObjectKey, bytes.NewReader(output.Data), int64(len(output.Data)), PutObjectOptions{
		ContentType: domain.ContentTypeWOFF2, ChecksumSHA256: checksumHex,
	})
	if err != nil {
		return "", fmt.Errorf("%w: upload artifact: %v", ErrObjectStorageUnavailable, err)
	}
	info.ChecksumSHA256 = checksumHex
	location, err = s.objects.PublicURL(buildCtx, artifact.ObjectKey)
	if err != nil {
		return "", fmt.Errorf("%w: resolve artifact URL: %v", ErrObjectStorageUnavailable, err)
	}
	if err := s.repository.MarkFontArtifactReady(buildCtx, artifact.Key, leaseOwner, domain.ArtifactObject{
		SizeBytes: info.SizeBytes, ETag: info.ETag, ChecksumSHA256: info.ChecksumSHA256,
	}); err != nil {
		if errors.Is(err, domain.ErrBuildNotReady) {
			return "", ErrBuildNotReady
		}
		return "", fmt.Errorf("mark artifact ready: %w", err)
	}
	artifactReady = true
	if err := s.repository.CompleteBuildJob(buildCtx, artifact.Key, leaseOwner); err != nil {
		if errors.Is(err, domain.ErrBuildNotReady) {
			return "", ErrBuildNotReady
		}
		return "", fmt.Errorf("complete build job: %w", err)
	}
	return location, nil
}

func (s *Service) fontSource(ctx context.Context, family domain.Family, weight int) (domain.Source, error) {
	source, err := s.repository.GetFontSource(ctx, family.ID, weight)
	if err == nil {
		if source.ObjectKey == "" {
			source.ObjectKey = domain.OriginalObjectKey(family.ID, weight, family.Format)
		}
		return s.enrichSourceFingerprint(ctx, source)
	}
	if !errors.Is(err, ErrFontSourceNotFound) && !errors.Is(err, domain.ErrSourceNotFound) {
		return domain.Source{}, fmt.Errorf("get font source: %w", err)
	}
	return s.enrichSourceFingerprint(ctx, domain.Source{
		FamilyID: family.ID, Weight: weight, Format: family.Format,
		ObjectKey:     domain.OriginalObjectKey(family.ID, weight, family.Format),
		SourceVersion: family.Version,
	})
}

func (s *Service) enrichSourceFingerprint(ctx context.Context, source domain.Source) (domain.Source, error) {
	if source.ChecksumSHA256 != "" {
		return source, nil
	}
	info, err := s.objects.StatObject(ctx, source.ObjectKey)
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			return domain.Source{}, fmt.Errorf("%w: %s", ErrFontSourceNotFound, source.ObjectKey)
		}
		return domain.Source{}, fmt.Errorf("%w: stat font source: %v", ErrObjectStorageUnavailable, err)
	}
	source.SizeBytes = info.SizeBytes
	switch {
	case info.ChecksumSHA256 != "":
		source.ChecksumSHA256 = normalizeChecksum(info.ChecksumSHA256)
	case info.ETag != "":
		source.SourceVersion = normalizeETag(info.ETag)
	}
	return source, nil
}

func successResponse(family domain.Family, weight int, mode string, locations []string) GenerateResponse {
	return GenerateResponse{Code: 200, Status: "success", Message: "", Location: locations, Name: family.ID, Weight: weight, BuildMode: mode}
}

func mapRepositoryError(operation string, err error) error {
	switch {
	case errors.Is(err, ErrFontNotFound), errors.Is(err, domain.ErrFontNotFound):
		return ErrFontNotFound
	case errors.Is(err, ErrFontSourceNotFound), errors.Is(err, domain.ErrSourceNotFound):
		return ErrFontSourceNotFound
	default:
		return fmt.Errorf("%s: %w", operation, err)
	}
}

func invalidInput(err error) error {
	return fmt.Errorf("%w: %w", ErrInvalidInput, err)
}

func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func truncateError(err error) string {
	message := err.Error()
	if len(message) > 2000 {
		return message[:2000]
	}
	return message
}

func cloneStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return append([]string(nil), values...)
}

func cloneInts(values []int) []int {
	if values == nil {
		return []int{}
	}
	return append([]int(nil), values...)
}

func cssFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "otf":
		return "opentype"
	case "woff2":
		return "woff2"
	default:
		return "truetype"
	}
}

func newLeaseOwner(workerID string) string {
	var token [8]byte
	if _, err := rand.Read(token[:]); err != nil {
		return workerID + ":" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return workerID + ":" + hex.EncodeToString(token[:])
}

func normalizeETag(value string) string {
	return strings.Trim(strings.TrimSpace(value), `"`)
}

func normalizeChecksum(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "sha256:")
	return value
}
