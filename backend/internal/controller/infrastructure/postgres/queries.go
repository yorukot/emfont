package postgres

import (
	"github.com/emfont/emfont/backend/internal/controller/infrastructure/postgres/sqlc"
)

func NewQueries(db sqlc.DBTX) *sqlc.Queries {
	return sqlc.New(db)
}
