package test

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// lookupPattern matches the first argument of every configuration reader, which
// is the environment variable name.
var lookupPattern = regexp.MustCompile(`\(lookup, "([A-Z0-9_]+)"`)

// TestEveryConfigurationVariableIsDocumented keeps the example file honest.
//
// A tunable that only exists in the source is a tunable nobody operating this
// system can find. The claim lease, the pool timeouts and the broker reconnect
// settings all change behaviour under load or during an outage, which is exactly
// when somebody goes looking for them.
func TestEveryConfigurationVariableIsDocumented(t *testing.T) {
	source, err := os.ReadFile("../internal/platform/config/config.go")
	if err != nil {
		t.Fatalf("read the configuration source: %v", err)
	}
	example, err := os.ReadFile("../.env.example")
	if err != nil {
		t.Fatalf("read the example environment file: %v", err)
	}

	matches := lookupPattern.FindAllSubmatch(source, -1)
	if len(matches) == 0 {
		t.Fatalf("no configuration variable was found, the check would pass vacuously")
	}

	documented := make(map[string]struct{})
	for _, line := range strings.Split(string(example), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		name, _, found := strings.Cut(trimmed, "=")
		if found {
			documented[strings.TrimSpace(name)] = struct{}{}
		}
	}

	var missing []string
	seen := make(map[string]struct{})
	for _, match := range matches {
		name := string(match[1])
		if _, already := seen[name]; already {
			continue
		}
		seen[name] = struct{}{}
		if _, ok := documented[name]; !ok {
			missing = append(missing, name)
		}
	}

	sort.Strings(missing)
	for _, name := range missing {
		t.Errorf("%s is read by the configuration and absent from .env.example", name)
	}
}
