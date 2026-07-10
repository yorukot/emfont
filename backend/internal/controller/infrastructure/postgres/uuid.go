package postgres

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

var ErrInvalidUUID = errors.New("invalid uuid")

// NewUUID returns a random RFC 4122 version 4 UUID in pgx's native type.
func NewUUID() (pgtype.UUID, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return pgtype.UUID{}, fmt.Errorf("read uuid entropy: %w", err)
	}

	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80

	return pgtype.UUID{
		Bytes: bytes,
		Valid: true,
	}, nil
}

// ParseUUID parses a dashed or undashed UUID string into pgx's native type.
func ParseUUID(value string) (pgtype.UUID, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "-", "")
	if len(normalized) != 32 {
		return pgtype.UUID{}, fmt.Errorf("%w: %q", ErrInvalidUUID, value)
	}

	decoded, err := hex.DecodeString(normalized)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("%w: %q", ErrInvalidUUID, value)
	}

	var bytes [16]byte
	copy(bytes[:], decoded)
	return pgtype.UUID{
		Bytes: bytes,
		Valid: true,
	}, nil
}

// FormatUUID renders a pgx UUID as the canonical dashed lowercase string.
func FormatUUID(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}

	encoded := hex.EncodeToString(value.Bytes[:])
	return encoded[0:8] + "-" +
		encoded[8:12] + "-" +
		encoded[12:16] + "-" +
		encoded[16:20] + "-" +
		encoded[20:32]
}
