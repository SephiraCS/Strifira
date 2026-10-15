package apidocs

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/SephiraCS/strifira-backend/internal/dto"
)

// loadSpec parses the embedded document and returns its paths.
func loadSpec(t *testing.T) map[string]any {
	t.Helper()
	doc, err := Spec()
	if err != nil {
		t.Fatalf("embedded openapi.yaml does not parse: %v", err)
	}
	if doc["openapi"] != "3.1.0" {
		t.Fatalf("openapi = %v, want 3.1.0", doc["openapi"])
	}
	paths, ok := doc["paths"].(map[string]any)
	if !ok || len(paths) == 0 {
		t.Fatal("document has no paths")
	}
	return paths
}

var routeRe = regexp.MustCompile(`(?m)^\t(e|api|ws|auth|pub|dm|workers)\.(GET|POST|PATCH|PUT|DELETE|Any)\("([^"]*)",`)

var paramRe = regexp.MustCompile(`:(\w+)`)

// registeredOps extracts the (METHOD path) set from cmd/server/main.go,
// applying the group prefixes used there ("ws"/"dm" ride the workspace
// scope; an empty path there registers the workspace root itself).
func registeredOps(src string) map[string]bool {
	ops := map[string]bool{}
	for _, m := range routeRe.FindAllStringSubmatch(src, -1) {
		recv, method, path := m[1], strings.ToUpper(m[2]), m[3]
		if method == "ANY" {
			method = "POST"
		}
		switch recv {
		case "e":
		case "auth":
			path = "/api/auth" + path
		case "pub":
			path = "/api/public" + path
		case "api":
			path = "/api" + path
		case "workers":
			path = "/api/dev-box-worker" + path
		default: // ws / dm
			path = "/api/workspaces/:slug" + path
		}
		ops[method+" "+paramRe.ReplaceAllStringFunc(path, func(p string) string {
			return "{" + p[1:] + "}"
		})] = true
	}
	return ops
}

func TestRegisteredWorkerOperations(t *testing.T) {
	ops := registeredOps("\tworkers.POST(\"/jobs/:jobId/complete\", handler)\n")
	if !ops["POST /api/dev-box-worker/jobs/{jobId}/complete"] {
		t.Fatalf("worker route prefix not recognized: %v", ops)
	}
}

// TestRouteParity keeps the document in lockstep with the routes actually
// registered in cmd/server/main.go: every registration needs an entry and
// the document may not describe routes that do not exist. The floor on
// the match count makes a silent extractor break (a new registration
// style) loud instead of vacuously green.
func TestRouteParity(t *testing.T) {
	specOps := map[string]bool{}
	for path, item := range loadSpec(t) {
		for method, op := range item.(map[string]any) {
			switch method {
			case "get", "post", "patch", "put", "delete":
				if _, ok := op.(map[string]any); ok {
					specOps[strings.ToUpper(method)+" "+path] = true
				}
			}
		}
	}

	src, err := os.ReadFile("../../cmd/server/main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	regOps := registeredOps(string(src))
	if len(regOps) < 150 {
		t.Fatalf("extracted only %d route registrations from main.go; the extraction pattern is likely stale", len(regOps))
	}

	var missing, extra []string
	for op := range regOps {
		if !specOps[op] {
			missing = append(missing, op)
		}
	}
	for op := range specOps {
		if !regOps[op] {
			extra = append(extra, op)
		}
	}
	if len(missing) > 0 {
		t.Errorf("routes registered in main.go but absent from openapi.yaml:\n  %s", strings.Join(missing, "\n  "))
	}
	if len(extra) > 0 {
		t.Errorf("operations documented in openapi.yaml but not registered in main.go:\n  %s", strings.Join(extra, "\n  "))
	}
}

// TestRefsResolve catches dangling #/components/... references, which
// would render as empty schemas in the docs page.
func TestRefsResolve(t *testing.T) {
	doc, err := Spec()
	if err != nil {
		t.Fatalf("embedded openapi.yaml does not parse: %v", err)
	}
	dangling := collectDangling(doc, doc, "")
	if len(dangling) > 0 {
		t.Errorf("dangling $ref targets:\n  %s", strings.Join(dangling, "\n  "))
	}
}

func collectDangling(node, root any, path string) []string {
	var out []string
	switch v := node.(type) {
	case map[string]any:
		if ref, ok := v["$ref"].(string); ok && strings.HasPrefix(ref, "#/") {
			if !refExists(root, ref) {
				out = append(out, ref+" (at "+path+")")
			}
		}
		for k, child := range v {
			out = append(out, collectDangling(child, root, path+"/"+k)...)
		}
	case []any:
		for i, child := range v {
			out = append(out, collectDangling(child, root, path+"/"+string(rune('0'+i%10)))...)
		}
	}
	return out
}

func refExists(root any, ref string) bool {
	cur := root
	for _, seg := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		cur, ok = m[seg]
		if !ok {
			return false
		}
	}
	return true
}

// TestVersionMatchesUI keeps info.version in step with the fork's
// versioning rule: every deploying change bumps UI/package.json in the
// same commit, and the spec version rides along.
func TestVersionMatchesUI(t *testing.T) {
	raw, err := os.ReadFile("../../../UI/package.json")
	if err != nil {
		t.Skipf("UI/package.json not available in this checkout: %v", err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatalf("parse UI/package.json: %v", err)
	}
	doc, err := Spec()
	if err != nil {
		t.Fatalf("embedded openapi.yaml does not parse: %v", err)
	}
	info, _ := doc["info"].(map[string]any)
	if info["version"] != pkg.Version {
		t.Errorf("openapi info.version = %v, want %v (UI/package.json)", info["version"], pkg.Version)
	}
}

// Match the documented native job payload to the response type so the docs
// cannot promise step or timing data unavailable from GitHub's run-jobs API.
func TestGitHubActionJobSchemaMatchesResponse(t *testing.T) {
	doc, err := Spec()
	if err != nil {
		t.Fatal(err)
	}
	components := doc["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	schema := schemas["GitHubActionJob"].(map[string]any)
	properties := schema["properties"].(map[string]any)
	required := schema["required"].([]any)
	encoded, err := json.Marshal(dto.GitHubActionJobResponse{})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if len(properties) != len(fields) || len(required) != len(fields) {
		t.Fatalf("job schema fields = %v (required %v), response fields = %v", properties, required, fields)
	}
	for field := range fields {
		if _, exists := properties[field]; !exists {
			t.Errorf("job response field %q is undocumented", field)
		}
	}
	for _, field := range required {
		if _, exists := fields[field.(string)]; !exists {
			t.Errorf("job schema requires unsupported field %q", field)
		}
	}
	if _, exists := schemas["GitHubActionStep"]; exists {
		t.Error("job step schema remains despite the native API lacking step data")
	}
}
