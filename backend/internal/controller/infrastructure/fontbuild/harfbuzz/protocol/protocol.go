package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"regexp"
	"unicode/utf8"
)

const (
	Version  uint16 = 1
	Identity        = "worker-protocol-1"

	AbsoluteMaxSourceBytes     = int64(512 << 20)
	AbsoluteMaxOutputBytes     = int64(256 << 20)
	AbsoluteMaxCodepoints      = 4096
	AbsoluteMaxMessageBytes    = 4096
	AbsoluteMaxVersionBytes    = 256
	AbsoluteMaxDiagnosticBytes = int64(1 << 20)

	requestHeaderBytes  = 32
	responseHeaderBytes = 36
)

var (
	requestMagic                     = [8]byte{'E', 'M', 'F', 'O', 'N', 'T', 'W', 'Q'}
	responseMagic                    = [8]byte{'E', 'M', 'F', 'O', 'N', 'T', 'W', 'P'}
	productionBuilderIdentityPattern = regexp.MustCompile(
		`^harfbuzz-[0-9]+(?:\.[0-9]+){1,3}-woff2-[0-9]+(?:\.[0-9]+){1,3}` +
			`-worker-linux-(?:amd64|arm64)-go[0-9]+(?:\.[0-9]+){1,3}[A-Za-z0-9.+~_]*` +
			`-hb-[A-Za-z0-9.+:~_-]+-w2-[A-Za-z0-9.+:~_-]+` +
			`-src-[0-9a-f]{64}-pkg-[0-9a-f]{64}-worker-protocol-1$`,
	)
)

type Operation uint16

const (
	OperationVersion Operation = 1
	OperationSubset  Operation = 2
)

type Format uint16

const (
	FormatNone  Format = 0
	FormatWOFF2 Format = 1
)

type Status uint16

const (
	StatusOK Status = iota
	StatusInvalidRequest
	StatusUnsupportedCodepoints
	StatusNativeFailure
	StatusInternalFailure
	StatusResourceFailure
)

type Request struct {
	Operation    Operation
	Source       []byte
	Codepoints   []rune
	TargetFormat Format
}

type Response struct {
	Status         Status
	GlyphCount     uint32
	Data           []byte
	Message        string
	BuilderVersion string
}

// ValidateProductionBuilderIdentity validates the cache identity emitted by a
// worker built from the production Dockerfile. Package versions remain data,
// while the fixed fields make accidental development or partial identities
// fail closed in hardened environments.
func ValidateProductionBuilderIdentity(identity string) error {
	if len(identity) == 0 || len(identity) > AbsoluteMaxVersionBytes {
		return errors.New("production builder identity is outside the protocol length limit")
	}
	if !utf8.ValidString(identity) {
		return errors.New("production builder identity is not valid UTF-8")
	}
	if !productionBuilderIdentityPattern.MatchString(identity) {
		return errors.New("production builder identity does not match the required native package grammar")
	}
	return nil
}

func NewRequestReader(request Request, maxSourceBytes int64) (io.Reader, error) {
	if maxSourceBytes <= 0 || maxSourceBytes > AbsoluteMaxSourceBytes {
		return nil, fmt.Errorf("invalid request source limit %d", maxSourceBytes)
	}
	if err := validateRequest(request, maxSourceBytes); err != nil {
		return nil, err
	}

	header := bytes.NewBuffer(make([]byte, 0, requestHeaderBytes))
	header.Write(requestMagic[:])
	_ = binary.Write(header, binary.BigEndian, Version)
	_ = binary.Write(header, binary.BigEndian, uint16(request.Operation))
	_ = binary.Write(header, binary.BigEndian, uint32(0))
	_ = binary.Write(header, binary.BigEndian, uint64(len(request.Source)))
	_ = binary.Write(header, binary.BigEndian, uint32(len(request.Codepoints)))
	_ = binary.Write(header, binary.BigEndian, uint16(request.TargetFormat))
	_ = binary.Write(header, binary.BigEndian, uint16(0))

	codepoints := bytes.NewBuffer(make([]byte, 0, len(request.Codepoints)*4))
	for _, codepoint := range request.Codepoints {
		_ = binary.Write(codepoints, binary.BigEndian, uint32(codepoint))
	}
	return io.MultiReader(header, codepoints, bytes.NewReader(request.Source)), nil
}

func EncodeRequest(writer io.Writer, request Request, maxSourceBytes int64) error {
	reader, err := NewRequestReader(request, maxSourceBytes)
	if err != nil {
		return err
	}
	_, err = io.Copy(writer, reader)
	return err
}

func DecodeRequest(reader io.Reader, maxSourceBytes int64) (Request, error) {
	if maxSourceBytes <= 0 || maxSourceBytes > AbsoluteMaxSourceBytes {
		return Request{}, fmt.Errorf("invalid request source limit %d", maxSourceBytes)
	}
	header := make([]byte, requestHeaderBytes)
	if _, err := io.ReadFull(reader, header); err != nil {
		return Request{}, fmt.Errorf("read request header: %w", err)
	}
	if !bytes.Equal(header[:8], requestMagic[:]) {
		return Request{}, errors.New("invalid request magic")
	}
	version := binary.BigEndian.Uint16(header[8:10])
	operation := Operation(binary.BigEndian.Uint16(header[10:12]))
	flags := binary.BigEndian.Uint32(header[12:16])
	sourceLength := binary.BigEndian.Uint64(header[16:24])
	codepointCount := binary.BigEndian.Uint32(header[24:28])
	format := Format(binary.BigEndian.Uint16(header[28:30]))
	reserved := binary.BigEndian.Uint16(header[30:32])
	if version != Version {
		return Request{}, fmt.Errorf("unsupported request protocol version %d", version)
	}
	if flags != 0 || reserved != 0 {
		return Request{}, errors.New("request reserved fields must be zero")
	}
	if sourceLength > uint64(maxSourceBytes) || sourceLength > uint64(AbsoluteMaxSourceBytes) {
		return Request{}, fmt.Errorf("request source length %d exceeds limit", sourceLength)
	}
	if codepointCount > AbsoluteMaxCodepoints {
		return Request{}, fmt.Errorf("request codepoint count %d exceeds limit", codepointCount)
	}
	switch operation {
	case OperationVersion:
		if sourceLength != 0 || codepointCount != 0 || format != FormatNone {
			return Request{}, errors.New("version request must not contain a build payload")
		}
	case OperationSubset:
		if sourceLength == 0 || codepointCount == 0 || format != FormatWOFF2 {
			return Request{}, errors.New("subset request header is invalid")
		}
	default:
		return Request{}, fmt.Errorf("unknown request operation %d", operation)
	}

	request := Request{Operation: operation, TargetFormat: format}
	if codepointCount > 0 {
		request.Codepoints = make([]rune, codepointCount)
		encoded := make([]byte, 4)
		for index := range request.Codepoints {
			if _, err := io.ReadFull(reader, encoded); err != nil {
				return Request{}, fmt.Errorf("read request codepoint %d: %w", index, err)
			}
			codepoint := rune(binary.BigEndian.Uint32(encoded))
			if !utf8.ValidRune(codepoint) {
				return Request{}, fmt.Errorf("request codepoint %d is not a Unicode scalar value", index)
			}
			request.Codepoints[index] = codepoint
		}
	}
	if sourceLength > 0 {
		request.Source = make([]byte, int(sourceLength))
		if _, err := io.ReadFull(reader, request.Source); err != nil {
			return Request{}, fmt.Errorf("read request source: %w", err)
		}
	}
	if err := requireEOF(reader); err != nil {
		return Request{}, fmt.Errorf("request trailing data: %w", err)
	}
	if err := validateRequest(request, maxSourceBytes); err != nil {
		return Request{}, err
	}
	return request, nil
}

func validateRequest(request Request, maxSourceBytes int64) error {
	switch request.Operation {
	case OperationVersion:
		if len(request.Source) != 0 || len(request.Codepoints) != 0 || request.TargetFormat != FormatNone {
			return errors.New("version request must not contain a build payload")
		}
	case OperationSubset:
		if len(request.Source) == 0 {
			return errors.New("subset request source is empty")
		}
		if int64(len(request.Source)) > maxSourceBytes || int64(len(request.Source)) > AbsoluteMaxSourceBytes {
			return errors.New("subset request source exceeds limit")
		}
		if len(request.Codepoints) == 0 || len(request.Codepoints) > AbsoluteMaxCodepoints {
			return errors.New("subset request codepoint count is outside the allowed range")
		}
		for _, codepoint := range request.Codepoints {
			if !utf8.ValidRune(codepoint) {
				return errors.New("subset request contains an invalid Unicode scalar value")
			}
		}
		if request.TargetFormat != FormatWOFF2 {
			return errors.New("subset request target format is not WOFF2")
		}
	default:
		return fmt.Errorf("unknown request operation %d", request.Operation)
	}
	return nil
}

func EncodeResponse(writer io.Writer, response Response, maxOutputBytes int64) error {
	if err := validateResponse(response, maxOutputBytes); err != nil {
		return err
	}
	header := bytes.NewBuffer(make([]byte, 0, responseHeaderBytes))
	header.Write(responseMagic[:])
	_ = binary.Write(header, binary.BigEndian, Version)
	_ = binary.Write(header, binary.BigEndian, uint16(response.Status))
	_ = binary.Write(header, binary.BigEndian, uint32(0))
	_ = binary.Write(header, binary.BigEndian, response.GlyphCount)
	_ = binary.Write(header, binary.BigEndian, uint64(len(response.Data)))
	_ = binary.Write(header, binary.BigEndian, uint32(len(response.Message)))
	_ = binary.Write(header, binary.BigEndian, uint16(len(response.BuilderVersion)))
	_ = binary.Write(header, binary.BigEndian, uint16(0))
	if err := writeBytes(writer, header.Bytes()); err != nil {
		return err
	}
	if err := writeBytes(writer, response.Data); err != nil {
		return err
	}
	if err := writeBytes(writer, []byte(response.Message)); err != nil {
		return err
	}
	return writeBytes(writer, []byte(response.BuilderVersion))
}

func DecodeResponse(reader io.Reader, maxOutputBytes int64) (Response, error) {
	if maxOutputBytes <= 0 || maxOutputBytes > AbsoluteMaxOutputBytes {
		return Response{}, fmt.Errorf("invalid response output limit %d", maxOutputBytes)
	}
	header := make([]byte, responseHeaderBytes)
	if _, err := io.ReadFull(reader, header); err != nil {
		return Response{}, fmt.Errorf("read response header: %w", err)
	}
	if !bytes.Equal(header[:8], responseMagic[:]) {
		return Response{}, errors.New("invalid response magic")
	}
	version := binary.BigEndian.Uint16(header[8:10])
	status := Status(binary.BigEndian.Uint16(header[10:12]))
	flags := binary.BigEndian.Uint32(header[12:16])
	glyphCount := binary.BigEndian.Uint32(header[16:20])
	dataLength := binary.BigEndian.Uint64(header[20:28])
	messageLength := binary.BigEndian.Uint32(header[28:32])
	versionLength := binary.BigEndian.Uint16(header[32:34])
	reserved := binary.BigEndian.Uint16(header[34:36])
	if version != Version {
		return Response{}, fmt.Errorf("unsupported response protocol version %d", version)
	}
	if flags != 0 || reserved != 0 {
		return Response{}, errors.New("response reserved fields must be zero")
	}
	if dataLength > uint64(maxOutputBytes) || dataLength > uint64(AbsoluteMaxOutputBytes) {
		return Response{}, fmt.Errorf("response data length %d exceeds limit", dataLength)
	}
	if messageLength > AbsoluteMaxMessageBytes {
		return Response{}, errors.New("response message exceeds limit")
	}
	if versionLength > AbsoluteMaxVersionBytes {
		return Response{}, errors.New("response builder version exceeds limit")
	}

	response := Response{Status: status, GlyphCount: glyphCount}
	if dataLength > 0 {
		response.Data = make([]byte, int(dataLength))
		if _, err := io.ReadFull(reader, response.Data); err != nil {
			return Response{}, fmt.Errorf("read response data: %w", err)
		}
	}
	message := make([]byte, int(messageLength))
	if _, err := io.ReadFull(reader, message); err != nil {
		return Response{}, fmt.Errorf("read response message: %w", err)
	}
	versionData := make([]byte, int(versionLength))
	if _, err := io.ReadFull(reader, versionData); err != nil {
		return Response{}, fmt.Errorf("read response builder version: %w", err)
	}
	if !utf8.Valid(message) || !utf8.Valid(versionData) {
		return Response{}, errors.New("response text fields are not valid UTF-8")
	}
	response.Message = string(message)
	response.BuilderVersion = string(versionData)
	if err := requireEOF(reader); err != nil {
		return Response{}, fmt.Errorf("response trailing data: %w", err)
	}
	if err := validateResponse(response, maxOutputBytes); err != nil {
		return Response{}, err
	}
	return response, nil
}

func validateResponse(response Response, maxOutputBytes int64) error {
	if maxOutputBytes <= 0 || maxOutputBytes > AbsoluteMaxOutputBytes {
		return errors.New("invalid response output limit")
	}
	if len(response.Data) > int(maxOutputBytes) || int64(len(response.Data)) > AbsoluteMaxOutputBytes {
		return errors.New("response data exceeds limit")
	}
	if len(response.Message) > AbsoluteMaxMessageBytes {
		return errors.New("response message exceeds limit")
	}
	if len(response.BuilderVersion) == 0 || len(response.BuilderVersion) > AbsoluteMaxVersionBytes {
		return errors.New("response builder version is outside the allowed range")
	}
	if !utf8.ValidString(response.Message) || !utf8.ValidString(response.BuilderVersion) {
		return errors.New("response text fields must be valid UTF-8")
	}
	if response.Status > StatusResourceFailure {
		return fmt.Errorf("unknown response status %d", response.Status)
	}
	if response.Status == StatusOK {
		if response.Message != "" {
			return errors.New("successful response must not contain an error message")
		}
	} else if len(response.Data) != 0 || response.GlyphCount != 0 || response.Message == "" {
		return errors.New("error response must contain only a non-empty message")
	}
	return nil
}

func requireEOF(reader io.Reader) error {
	var trailing [1]byte
	count, err := reader.Read(trailing[:])
	if count != 0 {
		return errors.New("unexpected bytes after payload")
	}
	if err == nil {
		return errors.New("reader did not terminate after payload")
	}
	if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func writeBytes(writer io.Writer, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	written, err := writer.Write(data)
	if err != nil {
		return err
	}
	if written != len(data) {
		return io.ErrShortWrite
	}
	return nil
}
