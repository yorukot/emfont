package system

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

const (
	DefaultID ID = "default"

	StatusReady       Status = "ready"
	StatusDegraded    Status = "degraded"
	StatusMaintenance Status = "maintenance"
)

var (
	ErrInvalidSystem  = errors.New("invalid system")
	ErrSystemNotFound = errors.New("system not found")
)

type ID string

type Environment string

type Status string

type VNInput struct {
	ID          string
	Name        string
	Environment string
	Version     string
	Revision    string
	Status      string
}

type System struct {
	id          ID
	name        string
	environment Environment
	version     string
	revision    string
	status      Status
}

func VN(input VNInput) (System, error) {
	var errs []error

	id, err := NormalizeID(input.ID)
	if err != nil {
		errs = append(errs, err)
	}

	name := normalizeLabel(input.Name)
	if name == "" {
		errs = append(errs, invalid("name", "must not be empty"))
	}
	if len(name) > 120 {
		errs = append(errs, invalid("name", "must be 120 characters or fewer"))
	}

	environment := Environment(normalizeToken(input.Environment))
	if environment == "" {
		errs = append(errs, invalid("environment", "must not be empty"))
	}
	if environment != "" && !validToken(string(environment), 64) {
		errs = append(errs, invalid("environment", "may contain only lowercase letters, digits, dots, underscores, and dashes"))
	}

	version := strings.TrimSpace(input.Version)
	if version == "" {
		errs = append(errs, invalid("version", "must not be empty"))
	}
	if len(version) > 80 {
		errs = append(errs, invalid("version", "must be 80 characters or fewer"))
	}

	revision := normalizeRevision(input.Revision)
	if len(revision) > 80 {
		errs = append(errs, invalid("revision", "must be 80 characters or fewer"))
	}

	status := Status(normalizeToken(input.Status))
	if status == "" {
		status = StatusReady
	}
	if !validStatus(status) {
		errs = append(errs, invalid("status", "must be ready, degraded, or maintenance"))
	}

	if err := errors.Join(errs...); err != nil {
		return System{}, err
	}

	return System{
		id:          id,
		name:        name,
		environment: environment,
		version:     version,
		revision:    revision,
		status:      status,
	}, nil
}

func NormalizeID(value string) (ID, error) {
	normalized := normalizeToken(value)
	if normalized == "" {
		normalized = string(DefaultID)
	}
	if !validToken(normalized, 64) {
		return "", invalid("id", "may contain only lowercase letters, digits, dots, underscores, and dashes")
	}
	return ID(normalized), nil
}

func (s System) ID() ID {
	return s.id
}

func (s System) Name() string {
	return s.name
}

func (s System) Environment() Environment {
	return s.environment
}

func (s System) Version() string {
	return s.version
}

func (s System) Revision() string {
	return s.revision
}

func (s System) Status() Status {
	return s.status
}

func (s Status) String() string {
	return string(s)
}

func (id ID) String() string {
	return string(id)
}

func (environment Environment) String() string {
	return string(environment)
}

func validStatus(status Status) bool {
	switch status {
	case StatusReady, StatusDegraded, StatusMaintenance:
		return true
	default:
		return false
	}
}

func normalizeToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeLabel(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func normalizeRevision(value string) string {
	return strings.TrimSpace(value)
}

func validToken(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '.' || r == '_' || r == '-' {
			continue
		}
		if unicode.IsSpace(r) {
			return false
		}
		return false
	}
	return true
}

func invalid(field, message string) error {
	return fmt.Errorf("%w: %s %s", ErrInvalidSystem, field, message)
}
