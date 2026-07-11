package font

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeWordSetAndDynamicHash(t *testing.T) {
	normalized, codepoints, err := NormalizeWordSet("CBACA")
	if err != nil {
		t.Fatalf("NormalizeWordSet: %v", err)
	}
	if normalized != "ABC" {
		t.Fatalf("normalized = %q, want ABC", normalized)
	}
	if len(codepoints) != 3 {
		t.Fatalf("codepoint count = %d, want 3", len(codepoints))
	}
	if got, want := DynamicWordHash("DemoFont", 400, normalized), "a8852ec99b90315888fdf89c26d96945bc777ddad8dc115fb4ab6a9d902674a2"; got != want {
		t.Fatalf("dynamic hash = %q, want %q", got, want)
	}
}

func TestNormalizationUsesJavaScriptUTF16OrderingAndJSONEscaping(t *testing.T) {
	normalized, _, err := NormalizeWordSet("\uE000𐀀")
	if err != nil {
		t.Fatalf("NormalizeWordSet: %v", err)
	}
	if normalized != "𐀀\uE000" {
		t.Fatalf("normalized = %q, want JavaScript UTF-16 order", normalized)
	}
	if got, want := DynamicWordHash("DemoFont", 400, "A&B"), "1363a9aaf860aad7904f29955c094eda4d36f7f3aa60da220f8eca1a50b0a109"; got != want {
		t.Fatalf("dynamic hash = %q, want %q", got, want)
	}
	if got, want := DynamicWordHash("DemoFont", 400, normalized), "3f5112d119d57e8ddce1ef2c442779a02e90fd1bbea36a0f6f144f468dfb8730"; got != want {
		t.Fatalf("supplementary dynamic hash = %q, want %q", got, want)
	}
}

func TestNormalizeWordSetLimitsTotalRunesBeforeDeduplication(t *testing.T) {
	value := strings.Repeat("A", MaxTextRunes+1)

	_, _, err := NormalizeWordSet(value)
	if !errors.Is(err, ErrInvalidFont) {
		t.Fatalf("NormalizeWordSet error = %v, want ErrInvalidFont", err)
	}
	if !strings.Contains(err.Error(), "4096 characters or fewer") {
		t.Fatalf("NormalizeWordSet error = %q, want total character limit", err)
	}
}

func TestNormalizeWordSetAcceptsTotalRuneLimit(t *testing.T) {
	value := strings.Repeat("A", MaxTextRunes)

	normalized, codepoints, err := NormalizeWordSet(value)
	if err != nil {
		t.Fatalf("NormalizeWordSet: %v", err)
	}
	if normalized != "A" {
		t.Fatalf("normalized = %q, want A", normalized)
	}
	if len(codepoints) != 1 || codepoints[0] != 'A' {
		t.Fatalf("codepoints = %q, want [A]", codepoints)
	}
}

func TestNormalizeWordSetLimitsInputBytesBeforeRuneCounting(t *testing.T) {
	value := strings.Repeat("A", 1<<20)

	_, _, err := NormalizeWordSet(value)
	if !errors.Is(err, ErrInvalidFont) {
		t.Fatalf("NormalizeWordSet error = %v, want ErrInvalidFont", err)
	}
	if !strings.Contains(err.Error(), "bytes or fewer") {
		t.Fatalf("NormalizeWordSet error = %q, want byte limit", err)
	}
}

func TestResolveWeightUsesClosestAvailableWeight(t *testing.T) {
	weight, err := ResolveWeight([]int{200, 400, 700}, 620)
	if err != nil {
		t.Fatalf("ResolveWeight: %v", err)
	}
	if weight != 700 {
		t.Fatalf("weight = %d, want 700", weight)
	}
}

func TestNormalizeIDPreservesLegacyCase(t *testing.T) {
	id, err := NormalizeID(" GenSekiGothicTC ")
	if err != nil {
		t.Fatalf("NormalizeID: %v", err)
	}
	if id != "GenSekiGothicTC" {
		t.Fatalf("id = %q, want case-preserving id", id)
	}
}

func TestObjectKeysChangeWithSourceOrBuilderRevision(t *testing.T) {
	first := BuildRevision("source-a", "builder-a")
	second := BuildRevision("source-b", "builder-a")
	third := BuildRevision("source-a", "builder-b")
	if first == second || first == third {
		t.Fatalf("revisions are not unique: %q %q %q", first, second, third)
	}
	firstKey := DynamicObjectKey("hash", "DemoFont", 400, first)
	if firstKey == DynamicObjectKey("hash", "DemoFont", 400, second) {
		t.Fatal("source revision did not change dynamic object key")
	}
	if firstKey == DynamicObjectKey("hash", "DemoFont", 400, third) {
		t.Fatal("builder revision did not change dynamic object key")
	}
}

func TestSourceFingerprintIncludesExactObjectVersionAndChecksum(t *testing.T) {
	checksum := strings.Repeat("a", 64)
	base := Source{
		ObjectKey: "original-fonts/DemoFont/400.ttf", ObjectVersionID: "object-version-1",
		ChecksumSHA256: checksum, ETag: "etag-1", SizeBytes: 1024,
	}
	first := SourceFingerprint(base)
	secondSource := base
	secondSource.ObjectVersionID = "object-version-2"
	second := SourceFingerprint(secondSource)
	if first == second {
		t.Fatalf("source version replacement kept fingerprint %q", first)
	}
	firstArtifact := DynamicArtifactKey("word-hash", "DemoFont", 400, first, "builder")
	secondArtifact := DynamicArtifactKey("word-hash", "DemoFont", 400, second, "builder")
	if firstArtifact == secondArtifact {
		t.Fatalf("source version replacement kept artifact identity %q", firstArtifact)
	}
	if len(first) != 64 || len(second) != 64 {
		t.Fatalf("source fingerprints are not bounded SHA-256 values: %q %q", first, second)
	}
	changedChecksumSource := base
	changedChecksumSource.ChecksumSHA256 = strings.Repeat("b", 64)
	changedChecksum := SourceFingerprint(changedChecksumSource)
	if changedChecksum == first {
		t.Fatalf("source checksum change kept fingerprint %q", first)
	}
	changedKeySource := base
	changedKeySource.ObjectKey = "original-fonts/ReplacementFont/400.ttf"
	if changedKey := SourceFingerprint(changedKeySource); changedKey == first {
		t.Fatalf("source object key change kept fingerprint %q", first)
	}
	withoutChecksum := base
	withoutChecksum.ChecksumSHA256 = ""
	withoutChecksumFingerprint := SourceFingerprint(withoutChecksum)
	changedETagSource := withoutChecksum
	changedETagSource.ETag = "etag-2"
	if changedETag := SourceFingerprint(changedETagSource); changedETag == withoutChecksumFingerprint {
		t.Fatalf("source ETag change without a checksum kept fingerprint %q", withoutChecksumFingerprint)
	}
	changedSizeSource := withoutChecksum
	changedSizeSource.SizeBytes++
	if changedSize := SourceFingerprint(changedSizeSource); changedSize == withoutChecksumFingerprint {
		t.Fatalf("source size change without a checksum kept fingerprint %q", withoutChecksumFingerprint)
	}
}

func TestContentAddressedObjectKeyPreservesExtension(t *testing.T) {
	const checksum = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	got := ContentAddressedObjectKey("_generated/path/font.woff2", "sha256:"+checksum)
	want := "_generated/path/font-" + checksum + ".woff2"
	if got != want {
		t.Fatalf("ContentAddressedObjectKey = %q, want %q", got, want)
	}
}

func TestFencedContentAddressedObjectKeySeparatesBuildClaims(t *testing.T) {
	const checksum = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	first := FencedContentAddressedObjectKey("_generated/path/font.woff2", 41, checksum)
	second := FencedContentAddressedObjectKey("_generated/path/font.woff2", 42, checksum)
	if first == second {
		t.Fatal("different build fences produced the same object key")
	}
	if want := "_generated/path/font-f41-" + checksum + ".woff2"; first != want {
		t.Fatalf("first fenced object key = %q, want %q", first, want)
	}
}

func TestStaticIdentityIncludesPackContentFingerprint(t *testing.T) {
	first := StaticArtifactKey(100, "DemoFont", 400, "001", "pack-a", "source", "builder")
	second := StaticArtifactKey(100, "DemoFont", 400, "001", "pack-b", "source", "builder")
	if first == second {
		t.Fatal("pack content fingerprint did not change static artifact key")
	}
	firstObject := StaticObjectKey(100, "DemoFont", 400, "001", "pack-a", "revision")
	secondObject := StaticObjectKey(100, "DemoFont", 400, "001", "pack-b", "revision")
	if firstObject == secondObject {
		t.Fatal("pack content fingerprint did not change static object key")
	}
}
