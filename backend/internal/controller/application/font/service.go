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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	domain "github.com/emfont/emfont/backend/internal/domain/font"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"
)

const (
	defaultMaxPendingBuilds = 16
	defaultMaxSourceBytes   = int64(128 << 20)
	defaultArtifactTouch    = 5 * time.Minute
	defaultListLimit        = 100
	maxListLimit            = 500
	maxListSearchRunes      = 128
	maxStaticPacks          = 32
	maxStaticPackCodepoints = 256
)

type Config struct {
	BuilderVersion         string
	BuildLease             time.Duration
	BuildTimeout           time.Duration
	StaticBuildConcurrency int
	MaxPendingBuilds       int
	WorkerID               string
	ForceMin               bool
	MaxSourceBytes         int64
	ArtifactTouchInterval  time.Duration
}

type Service struct {
	repository Repository
	objects    ObjectStore
	builder    Builder
	config     Config
	builds     singleflight.Group
	buildSlots chan struct{}
	admission  chan struct{}
	observer   Observer

	lifecycleCtx    context.Context
	lifecycleCancel context.CancelFunc
	lifecycleMu     sync.Mutex
	shuttingDown    bool
	buildsWG        sync.WaitGroup
	shutdownDone    chan struct{}
}

type generatedSubset struct {
	family  domain.Family
	weight  int
	mode    string
	sources []generatedSource
}

type generatedSource struct {
	location   string
	codepoints []rune
}

type staticPackPlan struct {
	version    int
	number     int
	normalized string
	codepoints []rune
}

func NewService(repository Repository, objects ObjectStore, builder Builder, cfg Config, options ...Option) (*Service, error) {
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
	if cfg.MaxPendingBuilds <= 0 {
		cfg.MaxPendingBuilds = defaultMaxPendingBuilds
	}
	if cfg.MaxPendingBuilds < cfg.StaticBuildConcurrency {
		cfg.MaxPendingBuilds = cfg.StaticBuildConcurrency
	}
	if cfg.MaxSourceBytes <= 0 {
		cfg.MaxSourceBytes = defaultMaxSourceBytes
	}
	if cfg.ArtifactTouchInterval <= 0 {
		cfg.ArtifactTouchInterval = defaultArtifactTouch
	}
	if cfg.WorkerID == "" {
		hostname, _ := os.Hostname()
		cfg.WorkerID = hostname + "-" + strconv.Itoa(os.Getpid())
	}
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	service := &Service{
		repository:      repository,
		objects:         objects,
		builder:         builder,
		config:          cfg,
		buildSlots:      make(chan struct{}, cfg.StaticBuildConcurrency),
		admission:       make(chan struct{}, cfg.MaxPendingBuilds),
		lifecycleCtx:    lifecycleCtx,
		lifecycleCancel: lifecycleCancel,
		shutdownDone:    make(chan struct{}),
	}
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service, nil
}

func (s *Service) Shutdown(ctx context.Context) error {
	ctx = normalizeContext(ctx)
	s.lifecycleMu.Lock()
	if !s.shuttingDown {
		s.shuttingDown = true
		s.lifecycleCancel()
		go func() {
			s.buildsWG.Wait()
			close(s.shutdownDone)
		}()
	}
	done := s.shutdownDone
	s.lifecycleMu.Unlock()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error) {
	generated, err := s.generateSubset(ctx, req)
	if err != nil {
		return GenerateResponse{}, err
	}
	locations := make([]string, len(generated.sources))
	for index, source := range generated.sources {
		locations[index] = source.location
	}
	return successResponse(generated.family, generated.weight, generated.mode, locations), nil
}

func (s *Service) generateSubset(ctx context.Context, req GenerateRequest) (generatedSubset, error) {
	family, weight, normalized, codepoints, err := s.resolveRequest(ctx, req)
	if err != nil {
		return generatedSubset{}, err
	}

	if req.Min || s.config.ForceMin {
		location, err := s.generateDynamic(ctx, family, weight, normalized, codepoints)
		if err != nil {
			return generatedSubset{}, err
		}
		return generatedSubset{
			family: family, weight: weight, mode: domain.BuildModeDynamic,
			sources: []generatedSource{{location: location}},
		}, nil
	}

	sources, complete, err := s.generateStatic(ctx, family, weight, codepoints)
	if err != nil {
		return generatedSubset{}, err
	}
	if !complete {
		location, err := s.generateDynamic(ctx, family, weight, normalized, codepoints)
		if err != nil {
			return generatedSubset{}, err
		}
		return generatedSubset{
			family: family, weight: weight, mode: domain.BuildModeDynamic,
			sources: []generatedSource{{location: location}},
		}, nil
	}
	return generatedSubset{family: family, weight: weight, mode: domain.BuildModeStatic, sources: sources}, nil
}

func (s *Service) List(ctx context.Context, req ListRequest) (ListResult, error) {
	search := strings.TrimSpace(req.Search)
	if len([]rune(search)) > maxListSearchRunes {
		return ListResult{}, fmt.Errorf("%w: q must contain %d characters or fewer", ErrInvalidInput, maxListSearchRunes)
	}
	cursor := strings.TrimSpace(req.Cursor)
	if cursor != "" {
		normalizedCursor, err := domain.NormalizeID(cursor)
		if err != nil {
			return ListResult{}, invalidInput(err)
		}
		cursor = normalizedCursor
	}
	limit := req.Limit
	if limit == 0 {
		limit = defaultListLimit
	}
	if limit < 1 || limit > maxListLimit {
		return ListResult{}, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidInput, maxListLimit)
	}
	families, err := s.repository.ListFontFamilies(normalizeContext(ctx), search, cursor, limit+1)
	if err != nil {
		return ListResult{}, fmt.Errorf("list font families: %w", err)
	}
	nextCursor := ""
	if len(families) > limit {
		families = families[:limit]
		nextCursor = families[len(families)-1].ID
	}
	items := make([]FontListItem, 0, len(families))
	for _, family := range families {
		var author *string
		if len(family.Authors) > 0 {
			value := family.Authors[0]
			author = &value
		}
		items = append(items, FontListItem{
			ID: family.ID, Name: family.Name, Weight: cloneInts(family.Weights), Author: author,
			NameZH: family.NameZH, NameEN: family.NameEN, Category: family.Category,
			Tags: cloneStrings(family.Tags), Family: family.Family, SID: family.DemoContentID,
		})
	}
	return ListResult{Items: items, NextCursor: nextCursor}, nil
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
	if err := validateTargetFormat(req.Format); err != nil {
		return "", err
	}
	if strings.TrimSpace(req.Words) != "" {
		generated, err := s.generateSubset(ctx, req)
		if err != nil {
			return "", err
		}
		blocks := make([]string, 0, len(generated.sources))
		for _, source := range generated.sources {
			unicodeRange := ""
			if generated.mode == domain.BuildModeStatic {
				unicodeRange = cssUnicodeRange(source.codepoints)
				if unicodeRange == "" {
					return "", fmt.Errorf("%w: static pack has no codepoints", ErrBuildFailed)
				}
			}
			block := fmt.Sprintf("@font-face {\n  font-family: '%s';\n  src: url('%s') format('woff2');\n  font-weight: %d;\n  font-display: swap;\n",
				generated.family.ID, source.location, generated.weight,
			)
			if unicodeRange != "" {
				block += fmt.Sprintf("  unicode-range: %s;\n", unicodeRange)
			}
			blocks = append(blocks, block+"}\n")
		}
		return strings.Join(blocks, "\n"), nil
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
		if sourceErr := s.verifySourceAvailable(ctx, source); sourceErr != nil {
			return "", sourceErr
		}
		location, locationErr := s.objects.PublicURL(ctx, source.ObjectKey, source.ObjectVersionID)
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
	if err := validateTargetFormat(req.Format); err != nil {
		return domain.Family{}, 0, "", nil, err
	}
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
		BuilderVersion: s.config.BuilderVersion, ProtocolVersion: domain.ArtifactProtocolVersion,
		ObjectKey:   domain.DynamicObjectKey(wordHash, family.ID, weight, revision),
		ContentType: domain.ContentTypeWOFF2,
	}
	return s.ensureArtifact(ctx, source, artifact, codepoints)
}

func (s *Service) generateStatic(
	ctx context.Context,
	family domain.Family,
	weight int,
	requestedCodepoints []rune,
) ([]generatedSource, bool, error) {
	plan, complete, err := s.planStaticPacks(ctx, family.ID, requestedCodepoints)
	if err != nil || !complete {
		return nil, complete, err
	}
	source, err := s.fontSource(ctx, family, weight)
	if err != nil {
		return nil, false, err
	}
	sourceFingerprint := domain.SourceFingerprint(source)
	revision := domain.BuildRevision(sourceFingerprint, s.config.BuilderVersion)

	sources := make([]generatedSource, len(plan))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(s.config.StaticBuildConcurrency)
	for index, packPlan := range plan {
		index, packPlan := index, packPlan
		group.Go(func() error {
			pack := domain.PackID(packPlan.number)
			packFingerprint := domain.StaticWordHash(packPlan.normalized)
			artifact := domain.Artifact{
				Key: domain.StaticArtifactKey(
					packPlan.version, family.ID, weight, pack, packFingerprint, sourceFingerprint, s.config.BuilderVersion,
				),
				Kind: domain.BuildModeStatic, Status: "pending", FamilyID: family.ID, Weight: weight,
				Version: packPlan.version, Pack: pack, WordHash: packFingerprint,
				NormalizedWordSet: packPlan.normalized, SourceChecksum: sourceFingerprint,
				BuilderVersion: s.config.BuilderVersion, ProtocolVersion: domain.ArtifactProtocolVersion,
				ObjectKey: domain.StaticObjectKey(
					packPlan.version, family.ID, weight, pack, packFingerprint, revision,
				),
				ContentType: domain.ContentTypeWOFF2,
			}
			location, err := s.ensureArtifact(groupCtx, source, artifact, packPlan.codepoints)
			if err != nil {
				return err
			}
			sources[index] = generatedSource{location: location, codepoints: packPlan.codepoints}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, false, err
	}
	return sources, true, nil
}

func (s *Service) planStaticPacks(
	ctx context.Context,
	familyID string,
	requestedCodepoints []rune,
) ([]staticPackPlan, bool, error) {
	characters := make([]string, len(requestedCodepoints))
	for index, codepoint := range requestedCodepoints {
		characters[index] = string(codepoint)
	}
	snapshots, err := s.repository.GetStaticPackSnapshot(ctx, familyID, characters)
	if err != nil {
		return nil, false, fmt.Errorf("get static pack snapshot: %w", err)
	}
	if len(snapshots) == 0 || len(snapshots) > maxStaticPacks {
		return nil, false, nil
	}

	version := snapshots[0].Version
	seenPacks := make(map[int]struct{}, len(snapshots))
	covered := make(map[rune]struct{}, len(requestedCodepoints))
	plan := make([]staticPackPlan, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if !snapshot.CoverageComplete || snapshot.Version != version || snapshot.Number < 0 {
			return nil, false, nil
		}
		if _, duplicate := seenPacks[snapshot.Number]; duplicate {
			return nil, false, nil
		}
		seenPacks[snapshot.Number] = struct{}{}

		normalized, codepoints, valid := normalizeStaticPack(snapshot.Characters)
		if !valid {
			return nil, false, nil
		}
		for _, codepoint := range codepoints {
			covered[codepoint] = struct{}{}
		}
		plan = append(plan, staticPackPlan{
			version: version, number: snapshot.Number, normalized: normalized, codepoints: codepoints,
		})
	}
	for _, codepoint := range requestedCodepoints {
		if _, ok := covered[codepoint]; !ok {
			return nil, false, nil
		}
	}
	return plan, true, nil
}

func normalizeStaticPack(value string) (string, []rune, bool) {
	if value == "" || len(value) > maxStaticPackCodepoints*utf8.UTFMax || !utf8.ValidString(value) {
		return "", nil, false
	}
	runeCount := utf8.RuneCountInString(value)
	if runeCount == 0 || runeCount > maxStaticPackCodepoints {
		return "", nil, false
	}
	seen := make(map[rune]struct{}, runeCount)
	for _, codepoint := range value {
		seen[codepoint] = struct{}{}
	}
	if len(seen) > maxStaticPackCodepoints {
		return "", nil, false
	}
	codepoints := make([]rune, 0, len(seen))
	for codepoint := range seen {
		codepoints = append(codepoints, codepoint)
	}
	sort.Slice(codepoints, func(i, j int) bool {
		left := utf16.Encode([]rune{codepoints[i]})
		right := utf16.Encode([]rune{codepoints[j]})
		for index := 0; index < len(left) && index < len(right); index++ {
			if left[index] != right[index] {
				return left[index] < right[index]
			}
		}
		return len(left) < len(right)
	})
	return string(codepoints), codepoints, true
}

func cssUnicodeRange(codepoints []rune) string {
	if len(codepoints) == 0 {
		return ""
	}
	ordered := append([]rune(nil), codepoints...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	ranges := make([]string, 0, len(ordered))
	start, end := ordered[0], ordered[0]
	for _, codepoint := range ordered[1:] {
		switch {
		case codepoint == end:
			continue
		case codepoint == end+1:
			end = codepoint
		default:
			ranges = append(ranges, formatUnicodeRange(start, end))
			start, end = codepoint, codepoint
		}
	}
	ranges = append(ranges, formatUnicodeRange(start, end))
	return strings.Join(ranges, ", ")
}

func formatUnicodeRange(start, end rune) string {
	if start == end {
		return fmt.Sprintf("U+%X", start)
	}
	return fmt.Sprintf("U+%X-%X", start, end)
}

func (s *Service) ensureArtifact(ctx context.Context, source domain.Source, artifact domain.Artifact, codepoints []rune) (string, error) {
	if location, ready, err := s.readyArtifact(ctx, artifact); err != nil {
		s.observeCache("error")
		return "", err
	} else if ready {
		s.observeCache("hit")
		return location, nil
	}
	s.observeCache("miss")

	result := s.builds.DoChan(artifact.Key, func() (any, error) {
		buildCtx, cancel := s.newBuildContext(ctx)
		defer cancel()
		if !s.trackBuild() {
			return "", context.Canceled
		}
		defer s.buildsWG.Done()
		if err := buildCtx.Err(); err != nil {
			return "", err
		}
		select {
		case s.admission <- struct{}{}:
			s.observeBuildAdmission("accepted")
			s.observeBuildQueue()
			defer func() {
				<-s.admission
				s.observeBuildQueue()
			}()
		default:
			s.observeBuildAdmission("rejected")
			s.observeBuildQueue()
			return "", ErrBuildQueueFull
		}

		if location, ready, readyErr := s.readyArtifact(buildCtx, artifact); readyErr != nil {
			return "", readyErr
		} else if ready {
			return location, nil
		}
		if err := s.repository.CreateFontArtifact(buildCtx, artifact); err != nil {
			if errors.Is(err, domain.ErrArtifactCapacity) {
				return "", withRetryAfter(ErrArtifactCapacity, time.Second)
			}
			if errors.Is(err, domain.ErrTerminalFailureCached) {
				return "", ErrUnsupportedCodepoints
			}
			return "", fmt.Errorf("create font artifact: %w", err)
		}
		return s.buildArtifact(buildCtx, source, artifact, codepoints)
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

func (s *Service) newBuildContext(requestCtx context.Context) (context.Context, context.CancelFunc) {
	requestCtx = normalizeContext(requestCtx)
	buildCtx, cancel := context.WithTimeout(context.WithoutCancel(requestCtx), s.config.BuildTimeout)
	stopLifecycleCancel := context.AfterFunc(s.lifecycleCtx, cancel)
	if s.lifecycleCtx.Err() != nil {
		cancel()
	}
	return buildCtx, func() {
		stopLifecycleCancel()
		cancel()
	}
}

func (s *Service) trackBuild() bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.shuttingDown {
		return false
	}
	s.buildsWG.Add(1)
	return true
}

func (s *Service) readyArtifact(ctx context.Context, expected domain.Artifact) (string, bool, error) {
	return s.readyArtifactAttempt(ctx, expected, true)
}

func sameArtifactIdentity(actual, expected domain.Artifact) bool {
	return actual.Key == expected.Key &&
		actual.Kind == expected.Kind &&
		actual.FamilyID == expected.FamilyID &&
		actual.Weight == expected.Weight &&
		actual.Version == expected.Version &&
		actual.Pack == expected.Pack &&
		actual.WordHash == expected.WordHash &&
		actual.NormalizedWordSet == expected.NormalizedWordSet &&
		actual.SourceChecksum == expected.SourceChecksum &&
		actual.BuilderVersion == expected.BuilderVersion &&
		actual.ProtocolVersion == expected.ProtocolVersion &&
		actual.ContentType == expected.ContentType
}

func (s *Service) readyArtifactAttempt(
	ctx context.Context,
	expected domain.Artifact,
	retryOnCASFailure bool,
) (string, bool, error) {
	artifact, err := s.repository.GetFontArtifact(ctx, expected.Key)
	if err != nil {
		if errors.Is(err, ErrArtifactNotFound) || errors.Is(err, domain.ErrArtifactNotFound) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("get font artifact: %w", err)
	}
	if !sameArtifactIdentity(artifact, expected) {
		return "", false, fmt.Errorf("%w: stored artifact %q does not match the requested identity", domain.ErrArtifactConflict, expected.Key)
	}
	if artifact.Status == "failed" && artifact.FailureCode == domain.FailureCodeUnsupportedCodepoints {
		return "", false, ErrUnsupportedCodepoints
	}
	if artifact.Status != "ready" {
		return "", false, nil
	}
	markMissing := func(reason string) (string, bool, error) {
		marked, markErr := s.repository.MarkFontArtifactMissing(
			ctx, artifact.Key, artifact.ObjectKey, artifact.Generation, reason,
		)
		if markErr != nil {
			if errors.Is(markErr, domain.ErrArtifactCapacity) {
				return "", false, withRetryAfter(ErrArtifactCapacity, time.Second)
			}
			return "", false, fmt.Errorf("mark font artifact missing: %w", markErr)
		}
		if !marked {
			if retryOnCASFailure {
				location, ready, retryErr := s.readyArtifactAttempt(ctx, expected, false)
				if retryErr != nil {
					return "", false, retryErr
				}
				if ready {
					return location, true, nil
				}
			}
			return "", false, ErrBuildNotReady
		}
		return "", false, nil
	}
	info, err := s.objects.StatObject(ctx, artifact.ObjectKey, artifact.ObjectVersionID)
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			return markMissing("object missing from storage")
		}
		return "", false, fmt.Errorf("%w: stat artifact: %w", ErrObjectStorageUnavailable, err)
	}
	if info.SizeBytes <= 0 || artifact.SizeBytes > 0 && info.SizeBytes != artifact.SizeBytes {
		return markMissing("object size does not match artifact metadata")
	}
	expectedChecksum, expectedChecksumOK := canonicalSHA256(artifact.ChecksumSHA256)
	actualChecksum, actualChecksumOK := canonicalSHA256(info.ChecksumSHA256)
	if artifact.ProtocolVersion == domain.ArtifactProtocolVersion {
		if artifact.ObjectVersionID == "" {
			return markMissing("object version is missing from artifact metadata")
		}
		if !info.ChecksumVerified {
			return markMissing("object has no server-verified SHA-256 checksum")
		}
		if !expectedChecksumOK || !actualChecksumOK || expectedChecksum != actualChecksum {
			return markMissing("object checksum does not match artifact metadata")
		}
	} else if expectedChecksumOK {
		if !actualChecksumOK || expectedChecksum != actualChecksum {
			return markMissing("object checksum does not match artifact metadata")
		}
	} else if artifact.ETag != "" && normalizeETag(artifact.ETag) != normalizeETag(info.ETag) {
		return markMissing("object ETag does not match artifact metadata")
	}
	current, err := s.repository.TouchFontArtifact(
		ctx, artifact.Key, artifact.ObjectKey, artifact.Generation, s.config.ArtifactTouchInterval,
	)
	if err != nil {
		return "", false, fmt.Errorf("touch font artifact: %w", err)
	}
	if !current {
		if retryOnCASFailure {
			location, ready, retryErr := s.readyArtifactAttempt(ctx, expected, false)
			if retryErr != nil {
				return "", false, retryErr
			}
			if ready {
				return location, true, nil
			}
		}
		return "", false, ErrBuildNotReady
	}
	location, err := s.objects.PublicURL(ctx, artifact.ObjectKey, artifact.ObjectVersionID)
	if err != nil {
		return "", false, fmt.Errorf("%w: resolve artifact URL: %w", ErrObjectStorageUnavailable, err)
	}
	return location, true, nil
}

func (s *Service) buildArtifact(ctx context.Context, source domain.Source, artifact domain.Artifact, codepoints []rune) (location string, err error) {
	started := time.Now()
	defer func() {
		s.observeBuild(artifact.Kind, buildOutcome(err), time.Since(started))
	}()
	select {
	case s.buildSlots <- struct{}{}:
		s.observeBuildQueue()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	slotHeld := true
	releaseBuildSlot := func() {
		if !slotHeld {
			return
		}
		<-s.buildSlots
		slotHeld = false
		s.observeBuildQueue()
	}
	defer releaseBuildSlot()

	if readyLocation, ready, readyErr := s.readyArtifact(ctx, artifact); readyErr != nil {
		return "", readyErr
	} else if ready {
		return readyLocation, nil
	}

	leaseOwner := newLeaseOwner(s.config.WorkerID)
	claim, acquired, err := s.repository.AcquireBuildJob(ctx, artifact.Key, leaseOwner, s.config.BuildLease)
	if err != nil {
		s.observeBuildLease("error")
		return "", fmt.Errorf("acquire build job: %w", err)
	}
	if !acquired {
		s.observeBuildLease("contended")
		if readyLocation, ready, readyErr := s.readyArtifact(ctx, artifact); readyErr != nil {
			return "", readyErr
		} else if ready {
			return readyLocation, nil
		}
		retryAfter, retryErr := s.repository.BuildRetryAfter(ctx, artifact.Key)
		if retryErr != nil {
			return "", fmt.Errorf("get build retry delay: %w", retryErr)
		}
		return "", withRetryAfter(ErrBuildNotReady, retryAfter)
	}
	s.observeBuildLease("acquired")

	jobFinalized := false
	defer func() {
		if err == nil {
			return
		}
		releaseBuildSlot()
		message := truncateError(err)
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cleanupCancel()
		if !jobFinalized {
			_ = s.repository.FailBuildJob(cleanupCtx, artifact.Key, claim, message)
		}
	}()
	if claim.Fence <= 0 || strings.TrimSpace(claim.Owner) == "" {
		return "", fmt.Errorf("%w: repository returned an invalid build claim", ErrBuildFailed)
	}

	reader, sourceInfo, err := s.objects.OpenObject(ctx, source.ObjectKey, source.ObjectVersionID)
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			return "", fmt.Errorf("%w: %s", ErrFontSourceNotFound, source.ObjectKey)
		}
		return "", fmt.Errorf("%w: open font source: %w", ErrObjectStorageUnavailable, err)
	}
	defer reader.Close()
	if err := verifySourceObject(source, sourceInfo); err != nil {
		return "", err
	}
	if sourceInfo.SizeBytes > s.config.MaxSourceBytes {
		return "", fmt.Errorf("%w: source font exceeds %d bytes", ErrBuildFailed, s.config.MaxSourceBytes)
	}
	sourceBytes, err := io.ReadAll(io.LimitReader(reader, s.config.MaxSourceBytes+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", fmt.Errorf("%w: read font source: %w", ErrObjectStorageUnavailable, err)
	}
	if int64(len(sourceBytes)) > s.config.MaxSourceBytes {
		return "", fmt.Errorf("%w: source font exceeds %d bytes", ErrBuildFailed, s.config.MaxSourceBytes)
	}
	if source.ChecksumSHA256 != "" {
		sourceChecksum := sha256.Sum256(sourceBytes)
		if source.ChecksumSHA256 != hex.EncodeToString(sourceChecksum[:]) {
			return "", fmt.Errorf("%w: source font checksum mismatch", ErrBuildFailed)
		}
	}
	currentSourceInfo, err := s.objects.StatObject(ctx, source.ObjectKey, source.ObjectVersionID)
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			return "", fmt.Errorf("%w: source font changed while it was being read", ErrBuildFailed)
		}
		return "", fmt.Errorf("%w: re-stat font source: %w", ErrObjectStorageUnavailable, err)
	}
	if !sameObjectVersion(sourceInfo, currentSourceInfo) {
		return "", fmt.Errorf("%w: source font changed while it was being read", ErrBuildFailed)
	}

	output, err := s.builder.BuildSubset(ctx, BuildInput{
		Source: sourceBytes, Codepoints: codepoints, SourceFormat: source.Format, TargetFormat: domain.OutputFormatWOFF2,
	})
	if err != nil {
		buildErr := fmt.Errorf("%w: %w", ErrBuildFailed, err)
		if errors.Is(err, ErrUnsupportedCodepoints) {
			if terminalErr := s.repository.FailBuildJobTerminal(
				ctx,
				artifact.Key,
				claim,
				domain.FailureCodeUnsupportedCodepoints,
				truncateError(buildErr),
			); terminalErr != nil {
				return "", fmt.Errorf("record terminal font build failure: %w", terminalErr)
			}
			jobFinalized = true
		}
		return "", buildErr
	}
	if len(output.Data) == 0 || output.GlyphCount == 0 {
		return "", fmt.Errorf("%w: subset contains no glyphs", ErrBuildFailed)
	}
	checksum := sha256.Sum256(output.Data)
	checksumHex := hex.EncodeToString(checksum[:])
	publishedObjectKey := domain.FencedContentAddressedObjectKey(artifact.ObjectKey, claim.Fence, checksumHex)
	info, err := s.objects.PutObject(ctx, publishedObjectKey, bytes.NewReader(output.Data), int64(len(output.Data)), PutObjectOptions{
		ContentType: domain.ContentTypeWOFF2, ChecksumSHA256: checksumHex,
	})
	if err != nil {
		return "", fmt.Errorf("%w: upload artifact: %w", ErrObjectStorageUnavailable, err)
	}
	info.ChecksumSHA256 = checksumHex
	location, err = s.objects.PublicURL(ctx, publishedObjectKey, info.VersionID)
	if err != nil {
		return "", fmt.Errorf("%w: resolve artifact URL: %w", ErrObjectStorageUnavailable, err)
	}
	if err := s.repository.MarkFontArtifactReady(ctx, artifact.Key, claim, domain.ArtifactObject{
		ObjectKey: publishedObjectKey, VersionID: info.VersionID, SizeBytes: info.SizeBytes,
		ETag: info.ETag, ChecksumSHA256: info.ChecksumSHA256,
	}); err != nil {
		if errors.Is(err, domain.ErrArtifactCapacity) {
			return "", withRetryAfter(ErrArtifactCapacity, time.Second)
		}
		if errors.Is(err, domain.ErrBuildNotReady) {
			return "", ErrBuildNotReady
		}
		return "", fmt.Errorf("mark artifact ready: %w", err)
	}
	jobFinalized = true
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
		checksum, ok := canonicalSHA256(source.ChecksumSHA256)
		if !ok {
			return domain.Source{}, fmt.Errorf("%w: source font checksum metadata is malformed", ErrBuildFailed)
		}
		source.ChecksumSHA256 = checksum
	}
	info, err := s.objects.StatObject(ctx, source.ObjectKey, "")
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			return domain.Source{}, fmt.Errorf("%w: %s", ErrFontSourceNotFound, source.ObjectKey)
		}
		return domain.Source{}, fmt.Errorf("%w: stat font source: %v", ErrObjectStorageUnavailable, err)
	}
	if source.SizeBytes > 0 && info.SizeBytes != source.SizeBytes {
		return domain.Source{}, fmt.Errorf("%w: source font size does not match database metadata", ErrBuildFailed)
	}
	if strings.TrimSpace(info.VersionID) == "" || info.VersionID == "null" {
		return domain.Source{}, fmt.Errorf("%w: source font has no stable object version", ErrBuildFailed)
	}
	source.SizeBytes = info.SizeBytes
	source.ObjectVersionID = info.VersionID
	source.ETag = normalizeETag(info.ETag)
	objectChecksum := ""
	if info.ChecksumSHA256 != "" {
		var ok bool
		objectChecksum, ok = canonicalSHA256(info.ChecksumSHA256)
		if !ok {
			return domain.Source{}, fmt.Errorf("%w: source object checksum metadata is malformed", ErrBuildFailed)
		}
	}
	if source.ChecksumSHA256 != "" && objectChecksum != "" &&
		!strings.EqualFold(source.ChecksumSHA256, objectChecksum) {
		return domain.Source{}, fmt.Errorf("%w: source font checksum does not match database metadata", ErrBuildFailed)
	}
	switch {
	case source.ChecksumSHA256 != "":
	case objectChecksum != "":
		source.ChecksumSHA256 = objectChecksum
	case source.ETag != "":
		source.SourceVersion = source.ETag
	default:
		return domain.Source{}, fmt.Errorf("%w: source font has no stable checksum or ETag", ErrBuildFailed)
	}
	return source, nil
}

func (s *Service) verifySourceAvailable(ctx context.Context, source domain.Source) error {
	info, err := s.objects.StatObject(ctx, source.ObjectKey, source.ObjectVersionID)
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			return fmt.Errorf("%w: %s", ErrFontSourceNotFound, source.ObjectKey)
		}
		return fmt.Errorf("%w: stat font source: %v", ErrObjectStorageUnavailable, err)
	}
	return verifySourceObject(source, info)
}

func validateTargetFormat(value string) error {
	format := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(value, ".")))
	if format == "" || format == domain.OutputFormatWOFF2 {
		return nil
	}
	return fmt.Errorf("%w: format must be %s", ErrInvalidInput, domain.OutputFormatWOFF2)
}

func verifySourceObject(source domain.Source, info ObjectInfo) error {
	if source.ObjectVersionID == "" || info.VersionID != source.ObjectVersionID {
		return fmt.Errorf("%w: source font object version changed before it was read", ErrBuildFailed)
	}
	if info.SizeBytes <= 0 || source.SizeBytes > 0 && info.SizeBytes != source.SizeBytes {
		return fmt.Errorf("%w: source font size changed before it was read", ErrBuildFailed)
	}
	if source.ETag != "" && normalizeETag(info.ETag) != source.ETag {
		return fmt.Errorf("%w: source font ETag changed before it was read", ErrBuildFailed)
	}
	if source.ChecksumSHA256 != "" && info.ChecksumSHA256 != "" {
		checksum, ok := canonicalSHA256(info.ChecksumSHA256)
		if !ok || checksum != source.ChecksumSHA256 {
			return fmt.Errorf("%w: source font checksum changed before it was read", ErrBuildFailed)
		}
	}
	return nil
}

func sameObjectVersion(left, right ObjectInfo) bool {
	if left.Key != "" && right.Key != "" && left.Key != right.Key {
		return false
	}
	if left.SizeBytes != right.SizeBytes {
		return false
	}
	if left.VersionID != right.VersionID {
		return false
	}
	if normalizeETag(left.ETag) != normalizeETag(right.ETag) {
		return false
	}
	if !strings.EqualFold(normalizeChecksum(left.ChecksumSHA256), normalizeChecksum(right.ChecksumSHA256)) {
		return false
	}
	if !left.LastModified.IsZero() && !right.LastModified.IsZero() && !left.LastModified.Equal(right.LastModified) {
		return false
	}
	return true
}

func (s *Service) observeCache(result string) {
	if s.observer != nil {
		s.observer.ObserveFontCache(result)
	}
}

func (s *Service) observeBuildAdmission(result string) {
	if s.observer != nil {
		s.observer.ObserveFontBuildAdmission(result)
	}
}

func (s *Service) observeBuildQueue() {
	if s.observer == nil {
		return
	}
	active := len(s.buildSlots)
	queued := len(s.admission) - active
	if queued < 0 {
		queued = 0
	}
	s.observer.ObserveFontBuildQueue(active, queued)
}

func (s *Service) observeBuildLease(result string) {
	if s.observer != nil {
		s.observer.ObserveFontBuildLease(result)
	}
}

func (s *Service) observeBuild(kind, outcome string, duration time.Duration) {
	if s.observer != nil {
		s.observer.ObserveFontBuild(normalizeBuildKind(kind), outcome, duration)
	}
}

func normalizeBuildKind(kind string) string {
	switch kind {
	case domain.BuildModeDynamic, domain.BuildModeStatic:
		return kind
	default:
		return "unknown"
	}
}

func buildOutcome(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, ErrBuildNotReady):
		return "contended"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, ErrObjectStorageUnavailable):
		return "storage_error"
	case errors.Is(err, ErrUnsupportedCodepoints):
		return "unsupported"
	case errors.Is(err, ErrBuildFailed):
		return "failed"
	default:
		return "error"
	}
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
	return strings.ToLower(value)
}

func canonicalSHA256(value string) (string, bool) {
	normalized := normalizeChecksum(value)
	decoded, err := hex.DecodeString(normalized)
	if err != nil || len(decoded) != sha256.Size {
		return "", false
	}
	return hex.EncodeToString(decoded), true
}
