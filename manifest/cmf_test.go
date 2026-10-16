package manifest

import (
	"testing"

	"github.com/ollama/ollama/types/model"
)

func TestCMFMetadataAndRunnerSelection(t *testing.T) {
	runner, format := MetadataForConfig(model.ConfigV2{ModelFormat: "CMF"})
	if runner != RunnerCMF {
		t.Fatalf("runner = %q, want %q", runner, RunnerCMF)
	}
	if format != FormatCMF {
		t.Fatalf("format = %q, want %q", format, FormatCMF)
	}

	child := Manifest{
		SchemaVersion: 2,
		MediaType:     MediaTypeManifest,
		Runner:        RunnerCMF,
		Format:        FormatCMF,
	}
	selected, err := selectManifestReferenceWithPreferences([]Manifest{child}, runnerPreferences())
	if err != nil {
		t.Fatal(err)
	}
	if selected.Runner != RunnerCMF {
		t.Fatalf("selected runner = %q, want %q", selected.Runner, RunnerCMF)
	}
	if selected.Format != FormatCMF {
		t.Fatalf("selected format = %q, want %q", selected.Format, FormatCMF)
	}
}

func TestCMFRunnerAliasesSelectManifestListChild(t *testing.T) {
	for _, alias := range []string{"cmf", "cmfrunner", "cmf-runner", "cmf_runner", "cortiq", "  Cortiq  "} {
		t.Run(alias, func(t *testing.T) {
			child := Manifest{
				SchemaVersion: 2,
				MediaType:     MediaTypeManifest,
				Runner:        alias,
				Format:        FormatCMF,
			}
			selected, err := SelectManifestReferenceForRunner([]Manifest{child}, RunnerCMF)
			if err != nil {
				t.Fatalf("SelectManifestReferenceForRunner(%q): %v", alias, err)
			}
			if selected.Runner != RunnerCMF {
				t.Fatalf("selected runner = %q, want canonical %q", selected.Runner, RunnerCMF)
			}
		})
	}
}
