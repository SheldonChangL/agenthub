package process

import (
	"testing"

	"agenthub.local/agenthub/internal/model"
)

func TestClassifyNamesRecognizesProviderExecutables(t *testing.T) {
	got := classifyNames([]string{
		"/usr/local/bin/claude",
		"C:\\Program Files\\Codex\\codex.exe",
		"ChatGPT for Chrome",
	})

	if !got[model.ProviderClaude].Running {
		t.Fatal("Claude process not recognized")
	}
	if !got[model.ProviderCodex].Running {
		t.Fatal("Codex process not recognized")
	}
}

func TestClassifyNamesDoesNotTreatBrowserExtensionAsCodex(t *testing.T) {
	got := classifyNames([]string{"ChatGPT for Chrome", "codex-helper"})

	if got[model.ProviderClaude].Running || got[model.ProviderCodex].Running {
		t.Fatalf("classifyNames() = %#v; want no providers", got)
	}
}
