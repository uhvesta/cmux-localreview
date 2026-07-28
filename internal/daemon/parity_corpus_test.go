package daemon

// This test intentionally has no dependency on Bun, Node, src/, or a running
// TypeScript daemon. Phase 0 used those tools once to capture the oracle;
// Phase 4 must be able to delete them while retaining a reviewable, pinned
// parity corpus in every Go/Bazel release build.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type frozenCorpusDescriptor struct {
	Filename    string
	SHA256      string
	GeneratedBy string
	FixtureMin  int
}

// These digests pin the final, checked-in captures. Updating an oracle is a
// deliberate compatibility change: reviewers must update both the capture and
// this test, rather than a release command recapturing mutable TS behaviour.
var frozenCorpusDescriptors = []frozenCorpusDescriptor{
	{
		Filename:    "http.json",
		SHA256:      "476ca5d5e5e8824a89a9da79ec571282bacca743e939ede5b082b00f2cce1aff",
		GeneratedBy: "scripts/capture-parity-fixtures.ts",
		FixtureMin:  1,
	},
	{
		Filename:    "remote-pr.json",
		SHA256:      "dbd2599fc42c62529f0c9f188852823dddb9170307136e657f1fd56f47d14aaa",
		GeneratedBy: "scripts/capture-remote-parity-fixtures.ts",
		FixtureMin:  1,
	},
}

type frozenCorpusDocument struct {
	Version     int    `json:"version"`
	Source      string `json:"source"`
	GeneratedBy string `json:"generatedBy"`
	Fixtures    []struct {
		Name string `json:"name"`
	} `json:"fixtures"`
}

func TestFrozenParityCorpusIntegrity(t *testing.T) {
	for _, descriptor := range frozenCorpusDescriptors {
		t.Run(descriptor.Filename, func(t *testing.T) {
			contents, err := os.ReadFile(findFrozenParityCorpusFile(t, descriptor.Filename))
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(contents)
			if got := hex.EncodeToString(sum[:]); got != descriptor.SHA256 {
				t.Fatalf("frozen corpus digest changed: got %s want %s; recapture is not part of release verification", got, descriptor.SHA256)
			}

			var document frozenCorpusDocument
			if err := json.Unmarshal(contents, &document); err != nil {
				t.Fatalf("invalid frozen corpus JSON: %v", err)
			}
			if document.Version != 1 || document.Source != "ts-final" || document.GeneratedBy != descriptor.GeneratedBy {
				t.Fatalf("invalid corpus provenance: version=%d source=%q generatedBy=%q", document.Version, document.Source, document.GeneratedBy)
			}
			if len(document.Fixtures) < descriptor.FixtureMin {
				t.Fatalf("corpus has %d fixtures; need at least %d", len(document.Fixtures), descriptor.FixtureMin)
			}
			seen := make(map[string]struct{}, len(document.Fixtures))
			for _, fixture := range document.Fixtures {
				if fixture.Name == "" {
					t.Fatal("corpus contains an unnamed fixture")
				}
				if _, duplicate := seen[fixture.Name]; duplicate {
					t.Fatalf("corpus contains duplicate fixture %q", fixture.Name)
				}
				seen[fixture.Name] = struct{}{}
			}
		})
	}
}

func findFrozenParityCorpusFile(t *testing.T, filename string) string {
	t.Helper()
	for dir, depth := ".", 0; depth < 8; dir, depth = filepath.Join(dir, ".."), depth+1 {
		candidate := filepath.Join(dir, "testdata", "parity", "ts-final", filename)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	t.Fatalf("could not locate frozen parity corpus %s; Bazel test must expose it as runfile data", filename)
	return ""
}
