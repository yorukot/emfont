package font

import "time"

// Observer must return quickly; build and request paths call it synchronously.
type Observer interface {
	ObserveFontCache(result string)
	ObserveFontBuildAdmission(result string)
	ObserveFontBuildQueue(active, queued int)
	ObserveFontBuildLease(result string)
	ObserveFontBuild(kind, outcome string, duration time.Duration)
}

type Option func(*Service)

func WithObserver(observer Observer) Option {
	return func(service *Service) {
		service.observer = observer
	}
}
