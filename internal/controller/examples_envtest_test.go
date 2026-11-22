/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// Every CR we ship for users to copy (examples/ and config/samples/) must be
// accepted by a real API server running the generated CRD, so the catalog can
// never drift from the schema (enums, minimums, required fields).
var _ = Describe("Shipped AgenticAutoscaler examples", func() {
	It("are all accepted by the API server's CRD schema", func() {
		var files []string
		for _, glob := range []string{"../../examples/*.yaml", "../../config/samples/*.yaml"} {
			m, err := filepath.Glob(glob)
			Expect(err).NotTo(HaveOccurred())
			files = append(files, m...)
		}

		created := 0
		for _, path := range files {
			raw, err := os.ReadFile(path)
			Expect(err).NotTo(HaveOccurred())
			for i, doc := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n---") {
				if !strings.Contains(doc, "kind: AgenticAutoscaler") {
					continue
				}
				// Create from the raw YAML, exactly as kubectl apply would send it
				// (a typed Go object would add empty value-struct fields).
				obj := &unstructured.Unstructured{}
				Expect(yaml.Unmarshal([]byte(doc), &obj.Object)).To(Succeed(), path)
				obj.SetNamespace("default")
				obj.SetName(fmt.Sprintf("example-%d-%d", created, i))
				Expect(k8sClient.Create(context.Background(), obj)).To(Succeed(),
					fmt.Sprintf("%s was rejected by the CRD schema", path))
				Expect(k8sClient.Delete(context.Background(), obj)).To(Succeed())
				created++
			}
		}
		Expect(created).To(BeNumerically(">", 5))
	})
})
