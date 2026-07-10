package font

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	DefaultWeight = 400
	MaxTextRunes  = 4096

	BuildModeDynamic = "dynamic"
	BuildModeStatic  = "static"

	OutputFormatWOFF2 = "woff2"
	ContentTypeWOFF2  = "font/woff2"

	DefaultBuilderVersion = "harfbuzz-woff2-v1"
)

var (
	ErrInvalidFont      = errors.New("invalid font")
	ErrFontNotFound     = errors.New("font not found")
	ErrSourceNotFound   = errors.New("font source not found")
	ErrArtifactNotFound = errors.New("font artifact not found")
	ErrBuildNotReady    = errors.New("font build not ready")
	ErrBuildFailed      = errors.New("font build failed")
)

type Family struct {
	ID            string
	Name          string
	NameZH        string
	NameEN        string
	Weights       []int
	License       string
	Version       string
	Description   string
	Category      string
	Family        string
	Tags          []string
	RepoURL       string
	Authors       []string
	Format        string
	DemoContentID int
}

type Source struct {
	FamilyID       string
	Weight         int
	Format         string
	ObjectKey      string
	ChecksumSHA256 string
	SizeBytes      int64
	SourceVersion  string
}

type Artifact struct {
	Key               string
	Kind              string
	Status            string
	FamilyID          string
	Weight            int
	Version           int
	Pack              string
	WordHash          string
	NormalizedWordSet string
	SourceChecksum    string
	BuilderVersion    string
	ObjectKey         string
	ContentType       string
	SizeBytes         int64
	ETag              string
	ChecksumSHA256    string
}

type ArtifactObject struct {
	SizeBytes      int64
	ETag           string
	ChecksumSHA256 string
}

func NormalizeID(value string) (string, error) {
	normalized := strings.TrimSpace(value)
	if normalized == "" {
		return "", invalid("font", "must not be empty")
	}
	if len(normalized) > 128 {
		return "", invalid("font", "must be 128 characters or fewer")
	}
	for _, r := range normalized {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			return "", invalid("font", "may contain only letters, digits, dots, underscores, and dashes")
		}
	}
	return normalized, nil
}

func NormalizeWeight(value string) (int, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "null") {
		return DefaultWeight, false, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, false, invalid("weight", "must be a number")
	}
	if parsed < 1 || parsed > 1000 {
		return 0, false, invalid("weight", "must be between 1 and 1000")
	}
	return parsed, true, nil
}

func ResolveWeight(available []int, requested int) (int, error) {
	if len(available) == 0 {
		return 0, ErrSourceNotFound
	}
	best := available[0]
	bestDistance := abs(best - requested)
	for _, candidate := range available[1:] {
		distance := abs(candidate - requested)
		if distance < bestDistance {
			best = candidate
			bestDistance = distance
		}
	}
	return best, nil
}

func NormalizeWordSet(value string) (string, []rune, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil, invalid("words", "must not be empty")
	}
	seen := make(map[rune]struct{}, utf8.RuneCountInString(value))
	for _, r := range value {
		seen[r] = struct{}{}
	}
	if len(seen) > MaxTextRunes {
		return "", nil, invalid("words", fmt.Sprintf("must contain %d unique characters or fewer", MaxTextRunes))
	}
	runes := make([]rune, 0, len(seen))
	for r := range seen {
		runes = append(runes, r)
	}
	sort.Slice(runes, func(i, j int) bool {
		left := utf16.Encode([]rune{runes[i]})
		right := utf16.Encode([]rune{runes[j]})
		for index := 0; index < len(left) && index < len(right); index++ {
			if left[index] != right[index] {
				return left[index] < right[index]
			}
		}
		return len(left) < len(right)
	})
	return string(runes), runes, nil
}

func DynamicWordHash(familyID string, weight int, normalizedWordSet string) string {
	var summary bytes.Buffer
	encoder := json.NewEncoder(&summary)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(struct {
		FontFamily string `json:"fontFamily"`
		FontWeight int    `json:"fontWeight"`
		WordSet    string `json:"wordSet"`
	}{
		FontFamily: familyID,
		FontWeight: weight,
		WordSet:    normalizedWordSet,
	})
	return SHA1Hex(strings.TrimSuffix(summary.String(), "\n"))
}

func StaticWordHash(normalizedWordSet string) string {
	return SHA1Hex(normalizedWordSet)
}

func SHA1Hex(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

func BuildRevision(sourceFingerprint, builderVersion string) string {
	return SHA1Hex(emptyToDash(sourceFingerprint) + ":" + emptyToDash(builderVersion))[:12]
}

func DynamicObjectKey(hash, familyID string, weight int, revision string) string {
	return fmt.Sprintf("_generated/%s-%s-%d-%s.woff2", hash, familyID, weight, revision)
}

func StaticObjectKey(version int, familyID string, weight int, pack, revision string) string {
	return fmt.Sprintf("_generated/%d-%s-%d-%s/%s.woff2", version, familyID, weight, revision, pack)
}

func OriginalObjectKey(familyID string, weight int, format string) string {
	format = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(format)), ".")
	if format == "" {
		format = "ttf"
	}
	return fmt.Sprintf("original-fonts/%s/%d.%s", familyID, weight, format)
}

func DynamicArtifactKey(hash, familyID string, weight int, sourceChecksum, builderVersion string) string {
	return strings.Join([]string{
		BuildModeDynamic,
		familyID,
		strconv.Itoa(weight),
		hash,
		emptyToDash(sourceChecksum),
		emptyToDash(builderVersion),
		OutputFormatWOFF2,
	}, ":")
}

func StaticArtifactKey(version int, familyID string, weight int, pack, sourceChecksum, builderVersion string) string {
	return strings.Join([]string{
		BuildModeStatic,
		strconv.Itoa(version),
		familyID,
		strconv.Itoa(weight),
		pack,
		emptyToDash(sourceChecksum),
		emptyToDash(builderVersion),
		OutputFormatWOFF2,
	}, ":")
}

func PackID(pack int) string {
	if pack < 0 {
		pack = 0
	}
	return fmt.Sprintf("%03d", pack)
}

func SourceFingerprint(source Source) string {
	switch {
	case source.ChecksumSHA256 != "":
		return source.ChecksumSHA256
	case source.SourceVersion != "":
		return source.SourceVersion
	case source.SizeBytes > 0:
		return strconv.FormatInt(source.SizeBytes, 10)
	default:
		return "unknown-source"
	}
}

func emptyToDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func invalid(field, message string) error {
	return fmt.Errorf("%w: %s %s", ErrInvalidFont, field, message)
}
