package protocol

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestRequestRoundTrip(t *testing.T) {
	want := Request{
		Operation: OperationSubset, Source: []byte("font-data"),
		Codepoints: []rune{'A', '\u6e2c'}, TargetFormat: FormatWOFF2,
	}
	var encoded bytes.Buffer
	if err := EncodeRequest(&encoded, want, 1024); err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	got, err := DecodeRequest(bytes.NewReader(encoded.Bytes()), 1024)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if got.Operation != want.Operation || got.TargetFormat != want.TargetFormat ||
		!bytes.Equal(got.Source, want.Source) || string(got.Codepoints) != string(want.Codepoints) {
		t.Fatalf("DecodeRequest = %#v, want %#v", got, want)
	}
}

func TestDecodeRequestRejectsMalformedAndTrailingData(t *testing.T) {
	valid := encodedRequest(t)
	tests := []struct {
		name   string
		mutate func([]byte) []byte
		match  string
	}{
		{name: "magic", mutate: func(data []byte) []byte { data[0] = 'X'; return data }, match: "magic"},
		{name: "version", mutate: func(data []byte) []byte { binary.BigEndian.PutUint16(data[8:10], Version+1); return data }, match: "version"},
		{name: "reserved", mutate: func(data []byte) []byte { data[15] = 1; return data }, match: "reserved"},
		{name: "operation", mutate: func(data []byte) []byte { binary.BigEndian.PutUint16(data[10:12], 99); return data }, match: "operation"},
		{name: "source length", mutate: func(data []byte) []byte { binary.BigEndian.PutUint64(data[16:24], 2048); return data }, match: "source length"},
		{name: "codepoints", mutate: func(data []byte) []byte {
			binary.BigEndian.PutUint32(data[24:28], AbsoluteMaxCodepoints+1)
			return data
		}, match: "codepoint"},
		{name: "trailing", mutate: func(data []byte) []byte { return append(data, 0) }, match: "trailing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := test.mutate(append([]byte(nil), valid...))
			_, err := DecodeRequest(bytes.NewReader(data), 1024)
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("DecodeRequest error = %v, want %q", err, test.match)
			}
		})
	}
}

func TestEncodeResponseRejectsShortWrite(t *testing.T) {
	err := EncodeResponse(shortWriter{}, Response{
		Status: StatusOK, Data: []byte("font"), GlyphCount: 1, BuilderVersion: Identity,
	}, 1024)
	if err == nil || !strings.Contains(err.Error(), "short write") {
		t.Fatalf("EncodeResponse error = %v, want short write", err)
	}
}

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	return len(data) - 1, nil
}

func TestResponseRoundTrip(t *testing.T) {
	want := Response{
		Status: StatusOK, GlyphCount: 3, Data: []byte("wOF2font"),
		BuilderVersion: "harfbuzz-10.2.0-woff2-1.0.2-" + Identity,
	}
	var encoded bytes.Buffer
	if err := EncodeResponse(&encoded, want, 1024); err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}
	got, err := DecodeResponse(bytes.NewReader(encoded.Bytes()), 1024)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if got.Status != want.Status || got.GlyphCount != want.GlyphCount ||
		!bytes.Equal(got.Data, want.Data) || got.BuilderVersion != want.BuilderVersion {
		t.Fatalf("DecodeResponse = %#v, want %#v", got, want)
	}
}

func TestResponseBuilderIdentityLengthBoundaries(t *testing.T) {
	for _, length := range []int{1, AbsoluteMaxVersionBytes} {
		var encoded bytes.Buffer
		if err := EncodeResponse(&encoded, Response{
			Status: StatusOK, BuilderVersion: strings.Repeat("v", length),
		}, 1024); err != nil {
			t.Fatalf("EncodeResponse identity length %d: %v", length, err)
		}
		response, err := DecodeResponse(bytes.NewReader(encoded.Bytes()), 1024)
		if err != nil {
			t.Fatalf("DecodeResponse identity length %d: %v", length, err)
		}
		if len(response.BuilderVersion) != length {
			t.Fatalf("builder identity length = %d, want %d", len(response.BuilderVersion), length)
		}
	}

	for _, identity := range []string{"", strings.Repeat("v", AbsoluteMaxVersionBytes+1)} {
		if err := EncodeResponse(&bytes.Buffer{}, Response{
			Status: StatusOK, BuilderVersion: identity,
		}, 1024); err == nil {
			t.Fatalf("EncodeResponse accepted identity length %d", len(identity))
		}
	}
}

func TestValidateProductionBuilderIdentity(t *testing.T) {
	valid := productionIdentity("linux-amd64", "go1.26.5")
	if len(valid) > AbsoluteMaxVersionBytes-8 {
		t.Fatalf("production identity length = %d, want at least 8 bytes of protocol headroom", len(valid))
	}
	if err := ValidateProductionBuilderIdentity(valid); err != nil {
		t.Fatalf("ValidateProductionBuilderIdentity: %v", err)
	}
	if err := ValidateProductionBuilderIdentity(productionIdentity("linux-arm64", "go1.27rc1")); err != nil {
		t.Fatalf("ValidateProductionBuilderIdentity arm64 release candidate: %v", err)
	}

	invalid := []string{
		"development-" + Identity,
		strings.Replace(valid, "linux-amd64", "linux-riscv64", 1),
		strings.Replace(valid, "-pkg-", "-packages-", 1),
		strings.Replace(valid, strings.Repeat("a", 64), strings.Repeat("A", 64), 1),
		strings.TrimSuffix(valid, "-"+Identity),
		valid + "x",
		string([]byte{0xff}),
	}
	for _, identity := range invalid {
		if err := ValidateProductionBuilderIdentity(identity); err == nil {
			t.Fatalf("ValidateProductionBuilderIdentity accepted %q", identity)
		}
	}
}

func TestDecodeResponseRejectsMalformedLengthsAndTrailingData(t *testing.T) {
	valid := encodedResponse(t)
	tests := []struct {
		name   string
		mutate func([]byte) []byte
		match  string
	}{
		{name: "magic", mutate: func(data []byte) []byte { data[0] = 'X'; return data }, match: "magic"},
		{name: "version", mutate: func(data []byte) []byte { binary.BigEndian.PutUint16(data[8:10], Version+1); return data }, match: "version"},
		{name: "data length", mutate: func(data []byte) []byte { binary.BigEndian.PutUint64(data[20:28], 2048); return data }, match: "data length"},
		{name: "message length", mutate: func(data []byte) []byte {
			binary.BigEndian.PutUint32(data[28:32], AbsoluteMaxMessageBytes+1)
			return data
		}, match: "message"},
		{name: "builder version length", mutate: func(data []byte) []byte {
			binary.BigEndian.PutUint16(data[32:34], AbsoluteMaxVersionBytes+1)
			return data
		}, match: "builder version"},
		{name: "builder version UTF-8", mutate: func(data []byte) []byte {
			data[len(data)-len(Identity)] = 0xff
			return data
		}, match: "UTF-8"},
		{name: "trailing", mutate: func(data []byte) []byte { return append(data, 0) }, match: "trailing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := test.mutate(append([]byte(nil), valid...))
			_, err := DecodeResponse(bytes.NewReader(data), 1024)
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("DecodeResponse error = %v, want %q", err, test.match)
			}
		})
	}
}

func FuzzDecodeRequest(f *testing.F) {
	var valid bytes.Buffer
	if err := EncodeRequest(&valid, Request{
		Operation: OperationSubset, Source: []byte("font"),
		Codepoints: []rune{'A'}, TargetFormat: FormatWOFF2,
	}, 4096); err != nil {
		f.Fatal(err)
	}
	f.Add(valid.Bytes())
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = DecodeRequest(bytes.NewReader(data), 4096)
	})
}

func FuzzDecodeResponse(f *testing.F) {
	var valid bytes.Buffer
	if err := EncodeResponse(&valid, Response{
		Status: StatusOK, Data: []byte("font"), GlyphCount: 1, BuilderVersion: Identity,
	}, 4096); err != nil {
		f.Fatal(err)
	}
	f.Add(valid.Bytes())
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = DecodeResponse(bytes.NewReader(data), 4096)
	})
}

func encodedRequest(t *testing.T) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := EncodeRequest(&encoded, Request{
		Operation: OperationSubset, Source: []byte("font"), Codepoints: []rune{'A'}, TargetFormat: FormatWOFF2,
	}, 1024); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func encodedResponse(t *testing.T) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := EncodeResponse(&encoded, Response{
		Status: StatusOK, Data: []byte("font"), GlyphCount: 1, BuilderVersion: Identity,
	}, 1024); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func productionIdentity(platform, goVersion string) string {
	return "harfbuzz-10.2.0-woff2-1.0.2-worker-" + platform + "-" + goVersion +
		"-hb-10.2.0-1+deb13u1-w2-1.0.2-2+b2-src-" + strings.Repeat("a", 64) +
		"-pkg-" + strings.Repeat("b", 64) + "-" + Identity
}
