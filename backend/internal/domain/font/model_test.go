package font

import "testing"

func TestNormalizeWordSetAndLegacyDynamicHash(t *testing.T) {
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
	if got, want := DynamicWordHash("DemoFont", 400, normalized), "b65482f6053b15b66bcb94b991de915697e34f64"; got != want {
		t.Fatalf("dynamic hash = %q, want %q", got, want)
	}
}

func TestLegacyNormalizationUsesJavaScriptUTF16OrderingAndJSONEscaping(t *testing.T) {
	normalized, _, err := NormalizeWordSet("\uE000𐀀")
	if err != nil {
		t.Fatalf("NormalizeWordSet: %v", err)
	}
	if normalized != "𐀀\uE000" {
		t.Fatalf("normalized = %q, want JavaScript UTF-16 order", normalized)
	}
	if got, want := DynamicWordHash("DemoFont", 400, "A&B"), "f4bfc666b6e9e6b3e6fc072c780a1dde18a0adb7"; got != want {
		t.Fatalf("dynamic hash = %q, want %q", got, want)
	}
	if got, want := DynamicWordHash("DemoFont", 400, normalized), "bee3c45d281ebfd0588d21c4076c727bb1389f53"; got != want {
		t.Fatalf("supplementary dynamic hash = %q, want %q", got, want)
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
