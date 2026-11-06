// Package examples_test checks that every example spec parses, builds a
// plan and validates without errors against the fixture host inventory.
package examples_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/oleksdovz/pk3s/internal/plan"
	"github.com/oleksdovz/pk3s/internal/spec"
	"github.com/oleksdovz/pk3s/internal/testutil"
	"github.com/oleksdovz/pk3s/internal/validate"
)

func TestExamplesAreValid(t *testing.T) {
	files, _ := filepath.Glob("*.yaml")
	if len(files) < 5 {
		t.Fatalf("expected at least 5 examples, found %d", len(files))
	}
	inv := testutil.HPInventory(t)
	for _, f := range files {
		t.Run(f, func(t *testing.T) {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			s, err := spec.Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			fs := validate.Spec(s, inv)
			p, err := plan.Build(s, inv, "v1.37.2+k3s1")
			if err != nil {
				t.Fatal(err)
			}
			fs = append(fs, validate.Plan(p, inv, validate.Probes{})...)
			for _, x := range fs {
				if x.Severity == validate.Error {
					t.Errorf("%s", x)
				}
			}
		})
	}
}
