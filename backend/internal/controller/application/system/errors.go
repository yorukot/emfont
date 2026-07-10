package system

import (
	"errors"
	"fmt"

	domain "github.com/emfont/emfont/backend/internal/domain/system"
)

var (
	ErrInvalidInput = errors.New("invalid system input")
	ErrNotFound     = domain.ErrSystemNotFound
)

func invalidInput(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrInvalidInput, err)
}

func storeError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotFound) {
		return err
	}
	return fmt.Errorf("%s system: %w", operation, err)
}
