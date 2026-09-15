package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"access-terminal-cloud-api/database"
	"access-terminal-cloud-api/models"
)

// The public documentation (apidocs/openapi.yaml, served at
// https://accesslink.store/docs) against the server it describes.
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
	Info struct {
		Description string `yaml:"description"`
	} `yaml:"info"`
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
	if models.APIErrorDocBase != "https://accesslink.store/docs/errors/" {
		t.Fatalf("models.APIErrorDocBase = %q; the site generates pages under https://accesslink.store/docs/errors/", models.APIErrorDocBase)
	}
	if !strings.Contains(raw, models.APIErrorDocBase) {
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
		"docs.accesslink.store", // the retired subdomain
		"onrender.com",          // a hosting hostname
	} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("apidocs/openapi.yaml contains %q", forbidden)
		}
	}
}

// --- the guide's examples, run for real ------------------------------------------

// A tagged fence in the guide: ```bash {op=createMember} is a curl request for
// that operation, ```json {op=createMember status=201} the body it answers
// with. apidocs/scripts/validate-examples.mjs checks both against the
// schemas; this replays the requests through the real router, so a documented
// example is one that actually runs against the server as built.

type guideFence struct {
	lang, op, status, body string
}

func guideFences(description string) []guideFence {
	var out []guideFence
	lines := strings.Split(description, "\n")
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimLeft(lines[i], " \t")
		if !strings.HasPrefix(trimmed, "```") {
			continue
		}
		indent := lines[i][:len(lines[i])-len(trimmed)]
		info := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
		var body []string
		for i++; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "```" {
				break
			}
			body = append(body, strings.TrimPrefix(lines[i], indent))
		}
		open := strings.Index(info, "{")
		if open < 0 || !strings.HasSuffix(info, "}") {
			continue
		}
		f := guideFence{lang: strings.TrimSpace(info[:open]), body: strings.Join(body, "\n")}
		for _, kv := range strings.Fields(info[open+1 : len(info)-1]) {
			k, v, _ := strings.Cut(kv, "=")
			switch k {
			case "op":
				f.op = v
			case "status":
				f.status = v
			}
		}
		out = append(out, f)
	}
	return out
}

type curlRequest struct {
	method, url, body string
	headers           map[string]string
}

// parseCurl reads the shapes the guide uses: quoted arguments, -X, -H, -d,
// and backslash line continuations.
func parseCurl(text string) (curlRequest, bool) {
	joined := strings.ReplaceAll(strings.ReplaceAll(text, "\\\r\n", " "), "\\\n", " ")
	joined = strings.TrimSpace(joined)
	if !strings.HasPrefix(joined, "curl ") {
		return curlRequest{}, false
	}
	var tokens []string
	rest := joined[5:]
	for len(rest) > 0 {
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" {
			break
		}
		switch rest[0] {
		case '"', '\'':
			q := rest[0]
			end := strings.IndexByte(rest[1:], q)
			if end < 0 {
				return curlRequest{}, false
			}
			tokens = append(tokens, rest[1:1+end])
			rest = rest[end+2:]
		default:
			end := strings.IndexAny(rest, " \t")
			if end < 0 {
				end = len(rest)
			}
			tokens = append(tokens, rest[:end])
			rest = rest[end:]
		}
	}
	req := curlRequest{method: "GET", headers: map[string]string{}}
	for i := 0; i < len(tokens); i++ {
		switch tokens[i] {
		case "-X", "--request":
			i++
			req.method = strings.ToUpper(tokens[i])
		case "-H", "--header":
			i++
			name, value, _ := strings.Cut(tokens[i], ":")
			req.headers[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(value)
		case "-d", "--data", "--data-raw", "--data-binary":
			i++
			req.body = tokens[i]
			if req.method == "GET" {
				req.method = "POST"
			}
		default:
			if req.url == "" && !strings.HasPrefix(tokens[i], "-") {
				req.url = tokens[i]
			}
		}
	}
	return req, req.url != ""
}

const guideOrigin = "https://api.accesslink.store"
const guideKeyPlaceholder = "<ACCESSLINK_API_KEY>"

func TestOpenAPIGuideExamplesRunAgainstTheRouter(t *testing.T) {
	doc, _ := loadOpenAPIDocument(t)
	env := newTestEnv(t)
	companyID := operatorCompanyID(t, "one")
	// The scopes as the console issues them: ExpandScopes is what makes
	// members:write carry members:read, exactly as the guide says it does.
	scopes, err := models.ExpandScopes([]string{models.ScopeMembersWrite, models.ScopeAccessRead, models.ScopeSitesRead, models.ScopeEventsRead})
	if err != nil {
		t.Fatalf("expanding scopes: %v", err)
	}
	issued, err := database.IssueAPICredential(database.APICredentialIssueInput{
		CompanyID:   companyID,
		Name:        "docs-examples",
		Environment: models.APIEnvironmentLive,
		Scopes:      scopes,
	})
	if err != nil {
		t.Fatalf("issuing the example credential: %v", err)
	}

	fences := guideFences(doc.Info.Description)
	requests := map[string]curlRequest{}
	var order []string
	for _, f := range fences {
		if f.lang != "bash" {
			continue
		}
		req, ok := parseCurl(f.body)
		if !ok {
			t.Fatalf("guide example for %s is not a curl request the test can read", f.op)
		}
		if !strings.HasPrefix(req.url, guideOrigin+"/") {
			t.Fatalf("guide example for %s does not name %s: %s", f.op, guideOrigin, req.url)
		}
		requests[f.op] = req
		order = append(order, f.op)
	}
	if len(order) < 4 {
		t.Fatalf("only %d tagged curl examples in the guide", len(order))
	}

	send := func(req curlRequest, withKey, withIdempotency bool) (int, map[string]any) {
		t.Helper()
		path := strings.TrimPrefix(req.url, guideOrigin)
		r := httptest.NewRequest(req.method, path, strings.NewReader(req.body))
		for name, value := range req.headers {
			switch {
			case name == "authorization":
				if withKey {
					r.Header.Set("Authorization", strings.Replace(value, guideKeyPlaceholder, issued.Secret, 1))
				}
			case name == "idempotency-key":
				if withIdempotency {
					r.Header.Set("Idempotency-Key", value)
				}
			default:
				r.Header.Set(name, value)
			}
		}
		if strings.Contains(r.Header.Get("Authorization"), guideKeyPlaceholder) {
			t.Fatalf("the placeholder was not substituted")
		}
		w := httptest.NewRecorder()
		env.router.ServeHTTP(w, r)
		var body map[string]any
		if w.Body.Len() > 0 {
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("%s %s answered a body that is not a JSON object: %s", req.method, path, w.Body.String())
			}
		}
		return w.Code, body
	}

	keysOf := func(m map[string]any) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}

	// Every tagged response, checked the way its status says: a 2xx by running
	// the request as documented; a 401 by running it without the key; a 409 on
	// a create by running it a second time without the idempotency key. The
	// body's keys must be the documented body's keys.
	ran := 0
	for _, f := range fences {
		if f.lang != "json" {
			continue
		}
		req, ok := requests[f.op]
		if !ok {
			t.Fatalf("guide response for %s has no curl request to run", f.op)
		}
		var want map[string]any
		if err := json.Unmarshal([]byte(f.body), &want); err != nil {
			t.Fatalf("guide response for %s %s is not a JSON object: %v", f.op, f.status, err)
		}
		var status int
		var body map[string]any
		switch {
		case strings.HasPrefix(f.status, "2"):
			status, body = send(req, true, true)
		case f.status == "401":
			status, body = send(req, false, true)
		case f.status == "409" && req.method == "POST":
			status, body = send(req, true, false)
		default:
			t.Fatalf("guide response for %s %s: the test does not know how to provoke a %s", f.op, f.status, f.status)
		}
		ran++
		if strconv.Itoa(status) != f.status {
			t.Fatalf("guide example %s: documented %s, server answered %d: %v", f.op, f.status, status, body)
		}
		if got, wanted := keysOf(body), keysOf(want); strings.Join(got, ",") != strings.Join(wanted, ",") {
			t.Fatalf("guide example %s %s: response keys %v, documented %v", f.op, f.status, got, wanted)
		}
		if wantErr, ok := want["error"].(map[string]any); ok {
			gotErr, _ := body["error"].(map[string]any)
			if gotErr["code"] != wantErr["code"] || gotErr["type"] != wantErr["type"] {
				t.Fatalf("guide example %s %s: error %v/%v, documented %v/%v", f.op, f.status,
					gotErr["type"], gotErr["code"], wantErr["type"], wantErr["code"])
			}
			if !strings.HasPrefix(gotErr["doc_url"].(string), models.APIErrorDocBase) {
				t.Fatalf("guide example %s %s: doc_url %v is not under %s", f.op, f.status, gotErr["doc_url"], models.APIErrorDocBase)
			}
		}
	}
	if ran < 6 {
		t.Fatalf("only %d guide responses were exercised", ran)
	}
	// The member the flow created is real, with the documented default rule.
	var active bool
	scanRow(t, `SELECT active FROM people WHERE company_id = $1 AND external_id = 'MEM042'`, []any{companyID}, &active)
	if !active {
		t.Fatalf("the flow's member is not active after creation")
	}
}
