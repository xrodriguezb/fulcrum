// Package test holds checks that are about the shape of the codebase rather than
// about its behaviour.
package test

import (
	"os/exec"
	"strings"
	"testing"
)

const modulePath = "github.com/xrodriguezb/fulcrum"

// TestArchitectureDomainPackagesImportOnlyTheStandardLibrary is the mechanical
// form of the dependency rule in ADR 0001. A document claiming the boundary
// holds is worth less than a test that fails when it does not.
func TestArchitectureDomainPackagesImportOnlyTheStandardLibrary(t *testing.T) {
	domainPackages := packagesMatching(t, "/domain")
	if len(domainPackages) == 0 {
		t.Fatalf("no domain package was found, the check would pass vacuously")
	}

	for _, pkg := range domainPackages {
		t.Run(pkg, func(t *testing.T) {
			for _, dep := range dependenciesOf(t, pkg) {
				if dep == pkg || isStandardLibrary(dep) {
					continue
				}
				if strings.HasPrefix(dep, modulePath) && strings.HasSuffix(dep, "/domain") {
					// Domain packages may depend on each other. Nothing else.
					continue
				}
				t.Errorf("%s imports %s, which is outside the standard library and the domain layer", pkg, dep)
			}
		})
	}
}

// TestArchitectureApplicationDoesNotImportInfrastructure keeps the dependency
// arrow pointing inwards: application code talks to ports it defines, and the
// infrastructure implements them.
func TestArchitectureApplicationDoesNotImportInfrastructure(t *testing.T) {
	appPackages := packagesMatching(t, "/app")

	for _, pkg := range appPackages {
		t.Run(pkg, func(t *testing.T) {
			for _, dep := range dependenciesOf(t, pkg) {
				if strings.HasPrefix(dep, modulePath) && strings.Contains(dep, "/infra") {
					t.Errorf("%s imports %s, so the dependency arrow points outwards", pkg, dep)
				}
			}
		})
	}
}

// TestArchitectureDriversStayInTheirLayer stops a database or broker client from
// being reached for in a handler or a use case. Those clients belong to the
// platform packages and to the infrastructure adapters.
func TestArchitectureDriversStayInTheirLayer(t *testing.T) {
	forbidden := []string{
		"github.com/jackc/pgx",
		"github.com/nats-io",
		"database/sql",
	}

	for _, pkg := range packagesMatching(t, modulePath+"/internal") {
		if strings.Contains(pkg, "/infra") || strings.HasPrefix(pkg, modulePath+"/internal/platform") {
			continue
		}
		t.Run(pkg, func(t *testing.T) {
			for _, dep := range directImportsOf(t, pkg) {
				for _, prefix := range forbidden {
					if strings.HasPrefix(dep, prefix) {
						t.Errorf("%s imports %s directly, which belongs to the infrastructure layer", pkg, dep)
					}
				}
			}
		})
	}
}

// packagesMatching lists the module's packages whose import path contains the
// given fragment.
func packagesMatching(t *testing.T, fragment string) []string {
	t.Helper()

	// The pattern is absolute rather than ./... because the test process runs
	// with this directory as its working directory, and ./... would then list
	// only this package.
	out, err := exec.Command("go", "list", modulePath+"/...").Output()
	if err != nil {
		t.Fatalf("go list failed: %v", err)
	}

	var matched []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pkg := strings.TrimSpace(line)
		if pkg != "" && strings.Contains(pkg, fragment) {
			matched = append(matched, pkg)
		}
	}
	return matched
}

// dependenciesOf returns the transitive dependencies of a package, which is what
// catches a violation hidden two hops away.
func dependenciesOf(t *testing.T, pkg string) []string {
	t.Helper()
	return runGoList(t, "-deps", "-f", "{{.ImportPath}}", pkg)
}

// directImportsOf returns only what the package imports itself.
func directImportsOf(t *testing.T, pkg string) []string {
	t.Helper()
	return runGoList(t, "-f", "{{join .Imports \"\\n\"}}", pkg)
}

func runGoList(t *testing.T, args ...string) []string {
	t.Helper()

	out, err := exec.Command("go", append([]string{"list"}, args...)...).Output()
	if err != nil {
		t.Fatalf("go list %v failed: %v", args, err)
	}

	var values []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		value := strings.TrimSpace(line)
		if value != "" {
			values = append(values, value)
		}
	}
	return values
}

// isStandardLibrary reports whether an import path belongs to the standard
// library. Standard library paths have no dot in their first segment, which is
// the same rule the go tool applies.
func isStandardLibrary(importPath string) bool {
	first, _, _ := strings.Cut(importPath, "/")
	return !strings.Contains(first, ".")
}
