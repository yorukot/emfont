package system

import domain "github.com/emfont/emfont/backend/internal/domain/system"

type GetRequest struct {
	ID string
}

type UpsertRequest struct {
	ID          string
	Name        string
	Environment string
	Version     string
	Revision    string
	Status      string
}

type SystemDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Environment string `json:"environment"`
	Version     string `json:"version"`
	Revision    string `json:"revision,omitempty"`
	Status      string `json:"status"`
}

func (r UpsertRequest) domainInput() domain.VNInput {
	return domain.VNInput{
		ID:          r.ID,
		Name:        r.Name,
		Environment: r.Environment,
		Version:     r.Version,
		Revision:    r.Revision,
		Status:      r.Status,
	}
}

func toDTO(system domain.System) SystemDTO {
	return SystemDTO{
		ID:          system.ID().String(),
		Name:        system.Name(),
		Environment: system.Environment().String(),
		Version:     system.Version(),
		Revision:    system.Revision(),
		Status:      system.Status().String(),
	}
}
