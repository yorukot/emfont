package system

import (
	"context"

	domain "github.com/emfont/emfont/backend/internal/domain/system"
)

type Store interface {
	GetSystem(context.Context, domain.ID) (domain.System, error)
	UpsertSystem(context.Context, domain.System) error
}
