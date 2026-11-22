package v1alpha1

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// exampleCRFiles lists every YAML file that ships AgenticAutoscaler CRs for
// users to copy: the examples/ catalog and the config/samples the demos apply.
func exampleCRFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	for _, glob := range []string{"../../examples/*.yaml", "../../config/samples/*.yaml"} {
		m, err := filepath.Glob(glob)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	if len(files) == 0 {
		t.Fatal("no example files found")
	}
	return files
}

// agenticDocs returns the AgenticAutoscaler documents in a multi-doc YAML file.
func agenticDocs(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var docs []string
	for _, doc := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n---") {
		if strings.Contains(doc, "kind: AgenticAutoscaler") {
			docs = append(docs, doc)
		}
	}
	return docs
}

// Every shipped CR must decode strictly (a typo'd or unknown field fails here,
// where the API server would silently prune it) and be internally consistent.
// Schema-level rules (enums, minimums) are checked against a real API server by
// the envtest suite in internal/controller.
func TestExampleCRs_DecodeStrictlyAndAreConsistent(t *testing.T) {
	seen := 0
	for _, path := range exampleCRFiles(t) {
		for i, doc := range agenticDocs(t, path) {
			seen++
			var aa AgenticAutoscaler
			if err := yaml.UnmarshalStrict([]byte(doc), &aa); err != nil {
				t.Errorf("%s doc %d: %v", path, i, err)
				continue
			}
			s := aa.Spec
			if s.TargetDeployment == "" || s.PrometheusURL == "" {
				t.Errorf("%s: targetDeployment and prometheusURL are required", path)
			}
			if s.MinReplicas < 1 || s.MinReplicas > s.MaxReplicas {
				t.Errorf("%s: bounds [%d, %d] invalid", path, s.MinReplicas, s.MaxReplicas)
			}
			switch s.HPACoexistence.Mode {
			case "owner", "advisor":
			case "calibrated":
				if s.HPACoexistence.HPAName == "" {
					t.Errorf("%s: calibrated mode needs hpaName", path)
				}
			default:
				t.Errorf("%s: hpaCoexistence.mode %q must be set explicitly", path, s.HPACoexistence.Mode)
			}
			for _, p := range s.LogPatterns {
				if _, err := regexp.Compile(p.Regex); err != nil {
					t.Errorf("%s: logPattern %s: %v", path, p.Name, err)
				}
			}
		}
	}
	if seen == 0 {
		t.Fatal("no AgenticAutoscaler documents found")
	}
}

// Every shipped manifest must parse document by document. YAML anchors do not
// cross "---" boundaries, so an alias to another document only fails here (or
// at kubectl apply time) — never in the Go build.
func TestShippedManifests_Parse(t *testing.T) {
	var files []string
	for _, glob := range []string{"../../deploy/*/*.yaml", "../../config/samples/*.yaml", "../../examples/*.yaml"} {
		m, err := filepath.Glob(glob)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for i, doc := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n---") {
			var v map[string]interface{}
			if err := yaml.Unmarshal([]byte(doc), &v); err != nil {
				t.Errorf("%s document %d: %v", path, i, err)
			}
		}
	}
}
