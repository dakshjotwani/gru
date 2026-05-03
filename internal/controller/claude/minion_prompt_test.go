package claude

import (
	"strings"
	"testing"

	"github.com/dakshjotwani/gru/internal/controller"
)

func TestComposeExtraPrompt_MinionGetsBlurb(t *testing.T) {
	got := composeExtraPrompt(controller.LaunchOptions{Profile: "default"})
	if !strings.Contains(got, "gru link add") || !strings.Contains(got, "gru artifact add") {
		t.Fatalf("minion prompt missing surfacing CLI hints; got:\n%s", got)
	}
}

func TestComposeExtraPrompt_MinionPrependsToCallerExtra(t *testing.T) {
	skill := "## Project skill\nUse TDD.\n"
	got := composeExtraPrompt(controller.LaunchOptions{Profile: "default", ExtraPrompt: skill})
	if !strings.Contains(got, "gru link add") {
		t.Fatalf("expected minion blurb in composed prompt; got:\n%s", got)
	}
	if !strings.Contains(got, skill) {
		t.Fatalf("caller's ExtraPrompt was dropped; got:\n%s", got)
	}
	if strings.Index(got, "gru link add") > strings.Index(got, "Project skill") {
		t.Fatalf("minion blurb should come before caller's ExtraPrompt")
	}
}

func TestComposeExtraPrompt_JournalSkipsBlurb(t *testing.T) {
	journalPrompt := "You are **Gru** — the assistant ..."
	got := composeExtraPrompt(controller.LaunchOptions{Profile: "journal", ExtraPrompt: journalPrompt})
	if got != journalPrompt {
		t.Fatalf("journal launch should pass ExtraPrompt through unchanged; got:\n%s", got)
	}
	if strings.Contains(got, "gru link add") {
		t.Fatalf("journal launch must not include minion blurb")
	}
}
