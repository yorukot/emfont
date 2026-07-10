package system

import domain "github.com/emfont/emfont/backend/internal/domain/system"

func validateGetRequest(req GetRequest) (domain.ID, error) {
	id, err := domain.NormalizeID(req.ID)
	if err != nil {
		return "", invalidInput(err)
	}
	return id, nil
}

func validateUpsertRequest(req UpsertRequest) (domain.System, error) {
	system, err := domain.VN(req.domainInput())
	if err != nil {
		return domain.System{}, invalidInput(err)
	}
	return system, nil
}
