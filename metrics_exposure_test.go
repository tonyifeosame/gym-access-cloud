package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// /metrics exposure (SEC-11, SEC-12).
//
// The endpoint publishes fleet size, device states by status, tenant and person
// counts, sync backlog depth and the running build's version and commit. That is
// a useful reconnaissance picture of an installation, and it used to be served
// to anybody when METRICS_TOKEN happened to be unset.

// metricsRequest issues a scrape with whatever credential the caller supplies.
func metricsRequest(t *testing.T, headers map[string]string) int {
	t.Helper()
	req := httptest.NewRequest("GET", "/metrics", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	NewRouter().ServeHTTP(rec, req)
	return rec.Code
}

// TestMetricsFailClosedWithoutAToken is SEC-12.
//
// The old behaviour made the SAFE configuration the one you had to remember: a
// deployment that forgot the variable, or lost it in an environment rename,
// published everything and said nothing about it.
func TestMetricsFailClosedWithoutAToken(t *testing.T) {
	t.Setenv("METRICS_TOKEN", "")
	t.Setenv("METRICS_PUBLIC", "")

	if code := metricsRequest(t, nil); code != http.StatusUnauthorized {
		t.Errorf("unconfigured /metrics returned %d, want 401 -- it must not be "+
			"open merely because nobody set a token", code)
	}
}

// TestMetricsCanBeOpenedDeliberately. An installation that genuinely wants an
// unauthenticated endpoint says so, which is a decision somebody made on purpose
// and can be found by grepping the environment.
func TestMetricsCanBeOpenedDeliberately(t *testing.T) {
	t.Setenv("METRICS_TOKEN", "")
	t.Setenv("METRICS_PUBLIC", "true")

	// 200 when the database is reachable, 503 when the scrape itself failed.
	// Either proves the request got past authorization, which is what is under
	// test here.
	if code := metricsRequest(t, nil); code == http.StatusUnauthorized {
		t.Error("METRICS_PUBLIC=true did not open the endpoint")
	}
}

// TestMetricsTokenIsRequiredAndAccepted covers both headers the endpoint takes.
func TestMetricsTokenIsRequiredAndAccepted(t *testing.T) {
	const token = "a-long-enough-metrics-token-value"
	t.Setenv("METRICS_TOKEN", token)
	t.Setenv("METRICS_PUBLIC", "")

	if code := metricsRequest(t, nil); code != http.StatusUnauthorized {
		t.Errorf("no credential returned %d, want 401", code)
	}
	if code := metricsRequest(t, map[string]string{"X-Metrics-Token": "wrong"}); code != http.StatusUnauthorized {
		t.Errorf("a wrong token returned %d, want 401", code)
	}

	// A prefix of the real token must be refused. This is the case a
	// non-constant-time comparison leaks the length and content of.
	if code := metricsRequest(t, map[string]string{
		"X-Metrics-Token": token[:len(token)-1],
	}); code != http.StatusUnauthorized {
		t.Errorf("a prefix of the token returned %d, want 401", code)
	}

	for name, headers := range map[string]map[string]string{
		"bearer":        {"Authorization": "Bearer " + token},
		"custom header": {"X-Metrics-Token": token},
	} {
		if code := metricsRequest(t, headers); code == http.StatusUnauthorized {
			t.Errorf("a correct token via %s was refused", name)
		}
	}

	// METRICS_PUBLIC must NOT override a configured token. An installation that
	// has set a credential has said what it wants, and a stray environment
	// variable must not quietly undo it.
	t.Setenv("METRICS_PUBLIC", "true")
	if code := metricsRequest(t, nil); code != http.StatusUnauthorized {
		t.Errorf("METRICS_PUBLIC overrode a configured METRICS_TOKEN (got %d)", code)
	}
}
