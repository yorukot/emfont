package metrics

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func TestMiddlewareUsesFixedRouteLabelForNotFoundRequests(t *testing.T) {
	metrics := MustNew(Config{})
	handler := metrics.Middleware(chiRoutePattern)(http.NotFoundHandler())

	for _, path := range []string{
		"/missing/first",
		"/missing/second",
		"/another/unmatched/path",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		if response.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want %d", path, response.Code, http.StatusNotFound)
		}
	}

	assertRouteSeries(t, metrics, "emfont_http_requests_total", "unknown", 3)
	assertRouteSeries(t, metrics, "emfont_http_request_duration_seconds", "unknown", 3)
}

func TestMiddlewareKeepsMatchedRoutePattern(t *testing.T) {
	metrics := MustNew(Config{})
	router := chi.NewRouter()
	router.Use(metrics.Middleware(chiRoutePattern))
	router.Get("/fonts/{fontID}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	for _, path := range []string{"/fonts/first", "/fonts/second"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)

		if response.Code != http.StatusNoContent {
			t.Fatalf("GET %s status = %d, want %d", path, response.Code, http.StatusNoContent)
		}
	}

	assertRouteSeries(t, metrics, "emfont_http_requests_total", "/fonts/{fontID}", 2)
	assertRouteSeries(t, metrics, "emfont_http_request_duration_seconds", "/fonts/{fontID}", 2)
}

func TestMiddlewareBoundsHTTPMethodLabelCardinality(t *testing.T) {
	metrics := MustNew(Config{})
	handler := metrics.Middleware(nil)(http.NotFoundHandler())
	methods := []string{
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
		http.MethodHead,
		http.MethodOptions,
	}
	for index := 0; index < 100; index++ {
		methods = append(methods, fmt.Sprintf("CUSTOM-%d", index))
	}

	for _, method := range methods {
		request := httptest.NewRequest(method, "/missing", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
	}

	want := map[string]float64{
		http.MethodGet:     1,
		http.MethodPost:    1,
		http.MethodPut:     1,
		http.MethodPatch:   1,
		http.MethodDelete:  1,
		http.MethodHead:    1,
		http.MethodOptions: 1,
		"OTHER":            100,
	}
	assertMethodSeries(t, metrics, "emfont_http_requests_total", want)
	assertMethodSeries(t, metrics, "emfont_http_request_duration_seconds", want)
}

func TestFontObserverMetricsUseBoundedLabels(t *testing.T) {
	metrics := MustNew(Config{})
	metrics.ObserveFontCache("hit")
	metrics.ObserveFontCache("request-specific-value")
	metrics.ObserveFontBuildAdmission("rejected")
	metrics.ObserveFontBuildQueue(2, 3)
	metrics.ObserveFontBuildLease("contended")
	metrics.ObserveFontBuild("dynamic", "success", 25*time.Millisecond)
	metrics.ObserveFontBuild("dynamic", "unsupported", 2*time.Millisecond)
	metrics.ObserveFontBuild("untrusted-kind", "untrusted-outcome", time.Millisecond)

	assertMetricValue(t, metrics, "emfont_font_cache_lookups_total", map[string]string{"result": "hit"}, 1)
	assertMetricValue(t, metrics, "emfont_font_cache_lookups_total", map[string]string{"result": "unknown"}, 1)
	assertMetricValue(t, metrics, "emfont_font_build_admissions_total", map[string]string{"result": "rejected"}, 1)
	assertMetricValue(t, metrics, "emfont_font_builds_active", nil, 2)
	assertMetricValue(t, metrics, "emfont_font_builds_queued", nil, 3)
	assertMetricValue(t, metrics, "emfont_font_build_leases_total", map[string]string{"result": "contended"}, 1)
	assertMetricValue(t, metrics, "emfont_font_builds_total", map[string]string{"kind": "dynamic", "outcome": "success"}, 1)
	assertMetricValue(t, metrics, "emfont_font_builds_total", map[string]string{"kind": "dynamic", "outcome": "unsupported"}, 1)
	assertMetricValue(t, metrics, "emfont_font_builds_total", map[string]string{"kind": "unknown", "outcome": "unknown"}, 1)
	assertMetricValue(t, metrics, "emfont_font_build_duration_seconds", map[string]string{"kind": "dynamic", "outcome": "success"}, 1)
	assertMetricValue(t, metrics, "emfont_font_build_duration_seconds", map[string]string{"kind": "dynamic", "outcome": "unsupported"}, 1)
}

func chiRoutePattern(r *http.Request) string {
	routeContext := chi.RouteContext(r.Context())
	if routeContext == nil {
		return ""
	}
	return routeContext.RoutePattern()
}

func assertRouteSeries(t *testing.T, metrics *Metrics, familyName, wantRoute string, wantCount float64) {
	t.Helper()

	families, err := metrics.registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}

	for _, family := range families {
		if family.GetName() != familyName {
			continue
		}
		if len(family.Metric) != 1 {
			t.Fatalf("%s series count = %d, want 1", familyName, len(family.Metric))
		}

		metric := family.Metric[0]
		gotRoute := ""
		for _, label := range metric.Label {
			if label.GetName() == "route" {
				gotRoute = label.GetValue()
				break
			}
		}
		if gotRoute != wantRoute {
			t.Fatalf("%s route = %q, want %q", familyName, gotRoute, wantRoute)
		}

		var gotCount float64
		switch {
		case metric.Counter != nil:
			gotCount = metric.GetCounter().GetValue()
		case metric.Histogram != nil:
			gotCount = float64(metric.GetHistogram().GetSampleCount())
		default:
			t.Fatalf("%s has unsupported metric type", familyName)
		}
		if gotCount != wantCount {
			t.Fatalf("%s sample count = %v, want %v", familyName, gotCount, wantCount)
		}
		return
	}

	t.Fatalf("metric family %q not found", familyName)
}

func assertMetricValue(t *testing.T, metrics *Metrics, familyName string, labels map[string]string, want float64) {
	t.Helper()
	families, err := metrics.registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() != familyName {
			continue
		}
		for _, metric := range family.Metric {
			matched := true
			for name, value := range labels {
				found := false
				for _, label := range metric.Label {
					if label.GetName() == name && label.GetValue() == value {
						found = true
						break
					}
				}
				if !found {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
			var got float64
			switch {
			case metric.Counter != nil:
				got = metric.GetCounter().GetValue()
			case metric.Gauge != nil:
				got = metric.GetGauge().GetValue()
			case metric.Histogram != nil:
				got = float64(metric.GetHistogram().GetSampleCount())
			default:
				t.Fatalf("%s has unsupported metric type", familyName)
			}
			if got != want {
				t.Fatalf("%s labels=%v value=%v, want %v", familyName, labels, got, want)
			}
			return
		}
		t.Fatalf("%s labels=%v not found", familyName, labels)
	}
	t.Fatalf("metric family %q not found", familyName)
}

func assertMethodSeries(t *testing.T, metrics *Metrics, familyName string, want map[string]float64) {
	t.Helper()

	families, err := metrics.registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() != familyName {
			continue
		}
		if len(family.Metric) != len(want) {
			t.Fatalf("%s series count = %d, want %d", familyName, len(family.Metric), len(want))
		}
		for _, metric := range family.Metric {
			method := ""
			for _, label := range metric.Label {
				if label.GetName() == "method" {
					method = label.GetValue()
					break
				}
			}
			wantCount, ok := want[method]
			if !ok {
				t.Fatalf("%s has unexpected method label %q", familyName, method)
			}
			var gotCount float64
			switch {
			case metric.Counter != nil:
				gotCount = metric.GetCounter().GetValue()
			case metric.Histogram != nil:
				gotCount = float64(metric.GetHistogram().GetSampleCount())
			default:
				t.Fatalf("%s has unsupported metric type", familyName)
			}
			if gotCount != wantCount {
				t.Fatalf("%s method=%q sample count = %v, want %v", familyName, method, gotCount, wantCount)
			}
		}
		return
	}

	t.Fatalf("metric family %q not found", familyName)
}
