package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"access-terminal-cloud-api/models"
)

// The public documentation (apidocs/openapi.yaml, served at
// docs.accesslink.store) against the server it describes.
//
// THE DOCUMENT IS HELD TO THE ROUTER, NOT THE OTHER WAY ROUND. A route the
// document describes must be mounted, with that method, at that path; every
// mounted public route must be described; and the error catalogue the site
// publishes must be the one models.APIErrors serves, code for code, with the
// same type, status and doc_url base. A documented route the server does not
// have is an invented endpoint, which is the one thing a public reference must
// never contain -- so it fails the build here, before anything is published.
//
// The document's own validity (OpenAPI 3.1, resolvable references, examples
// and the house rules) is checked by apidocs/scripts/validate.mjs in the docs
// build; this test does what only the Go module can, because only it has the
// router and the error table to compare against.

type openAPIDocument struct {
	Paths      map[string]map[string]any `yaml:"paths"`
	ErrorCodes []struct {
		Code     string `yaml:"code"`
		Type     string `yaml:"type"`
		Status   int    `yaml:"status"`
		Message  string `yaml:"message"`
		Reserved bool   `yaml:"reserved"`
	} `yaml:"x-accesslink-error-codes"`
}

func loadOpenAPIDocument(t *testing.T) (openAPIDocument, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("apidocs", "openapi.yaml"))
	if err != nil {
		t.Fatalf("reading apidocs/openapi.yaml: %v", err)
	}
	var doc openAPIDocument
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing apidocs/openapi.yaml: %v", err)
	}
	if len(doc.Paths) == 0 {
		t.Fatalf("apidocs/openapi.yaml documents no paths")
	}
	return doc, string(raw)
}

// ginPath turns an OpenAPI path template into the form gin reports:
// /members/{member_id} -> /members/:member_id.
func ginPath(template string) string {
	segments := strings.Split(template, "/")
	for i, s := range segments {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			segments[i] = ":" + strings.TrimSuffix(strings.TrimPrefix(s, "{"), "}")
		}
	}
	return strings.Join(segments, "/")
}

var openAPIMethods = []string{"get", "post", "put", "patch", "delete"}

func TestOpenAPIDocumentsOnlyRoutesTheServerMounts(t *testing.T) {
	doc, _ := loadOpenAPIDocument(t)
	env := newTestEnv(t)

	mounted := map[string]bool{}
	for _, route := range env.router.Routes() {
		mounted[route.Method+" "+route.Path] = true
	}

	documented := 0
	for path, item := range doc.Paths {
		// The reference covers the public tree and the console slice that
		// fingerprint enrolment needs. Hardware trees -- the site-key API and
		// the device API -- carry credentials that must not be published and
		// are never described here.
		switch {
		case strings.HasPrefix(path, "/api/public/v1/"),
			strings.HasPrefix(path, "/api/v1/auth/"),
			strings.HasPrefix(path, "/api/v1/console/"):
		default:
			t.Errorf("%s: the public reference must not describe this tree", path)
		}
		for _, method := range openAPIMethods {
			if _, ok := item[method]; !ok {
				continue
			}
			documented++
			key := strings.ToUpper(method) + " " + ginPath(path)
			if !mounted[key] {
				t.Errorf("documented but not mounted: %s", key)
			}
		}
	}
	if documented < 15 {
		t.Fatalf("only %d operations documented; the reference has shrunk", documented)
	}
}

func TestOpenAPIDocumentsEveryPublicRoute(t *testing.T) {
	doc, _ := loadOpenAPIDocument(t)
	env := newTestEnv(t)

	documented := map[string]bool{}
	for path, item := range doc.Paths {
		for _, method := range openAPIMethods {
			if _, ok := item[method]; ok {
				documented[strings.ToUpper(method)+" "+ginPath(path)] = true
			}
		}
	}
	var missing []string
	for _, route := range env.router.Routes() {
		if !strings.HasPrefix(route.Path, "/api/public/") {
			continue
		}
		if !documented[route.Method+" "+route.Path] {
			missing = append(missing, route.Method+" "+route.Path)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("public routes the server mounts but the reference does not describe:\n  %s",
			strings.Join(missing, "\n  "))
	}
}

func TestOpenAPIErrorCatalogueIsTheServers(t *testing.T) {
	doc, raw := loadOpenAPIDocument(t)

	published := map[string]bool{}
	for _, e := range doc.ErrorCodes {
		if published[e.Code] {
			t.Errorf("error code %s is listed twice", e.Code)
		}
		published[e.Code] = true
		spec, known := models.APIErrors[e.Code]
		if !known {
			t.Errorf("error code %s is published but the server does not serve it", e.Code)
			continue
		}
		if string(spec.Type) != e.Type || spec.Status != e.Status || spec.Message != e.Message {
			t.Errorf("error code %s: published as %s/%d %q, served as %s/%d %q",
				e.Code, e.Type, e.Status, e.Message, spec.Type, spec.Status, spec.Message)
		}
	}
	for _, code := range models.AllAPIErrorCodes() {
		if !published[code] {
			t.Errorf("error code %s is served but has no page: every doc_url must resolve", code)
		}
	}
	// The doc_url the server builds is the page the site generates.
	if models.APIErrorDocBase != "https://docs.accesslink.store/errors/" {
		t.Fatalf("models.APIErrorDocBase = %q; the site generates pages under https://docs.accesslink.store/errors/", models.APIErrorDocBase)
	}
	if !strings.Contains(raw, "https://docs.accesslink.store/errors/") {
		t.Fatalf("the document never names the error-page base the server links to")
	}
}

func TestOpenAPIPublishesNothingSecret(t *testing.T) {
	_, raw := loadOpenAPIDocument(t)
	lower := strings.ToLower(raw)
	for _, forbidden := range []string{
		"atd_",                  // device credential prefix
		"ats_",                  // site provisioning key prefix
		"x-device-key",          // device authentication header
		"x-api-key",             // site-key header
		"/api/v1/devices",       // device tree
		"fingerprint_template:", // a biometric field, as a field
		"-----begin",            // key material
		"replicat",              // no replication claim: not shipped
	} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("apidocs/openapi.yaml contains %q", forbidden)
		}
	}
}
