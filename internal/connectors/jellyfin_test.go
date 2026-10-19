package connectors

import "testing"

func TestJellyfinTimestamps(t *testing.T) {
	// same second, different fraction lengths: text order would say the opposite
	if !jfLater("2026-10-18T13:36:05.860369Z", "2026-10-18T13:36:05.86Z") || jfLater("2026-10-18T13:36:05.86Z", "2026-10-18T13:36:05.860369Z") {
		t.Fatal("fraction digits")
	}
	if !jfLater("2026-10-18T13:36:05Z", "") || jfLater("", "2026-10-18T13:36:05Z") {
		t.Fatal("empty")
	}
	if got := withButton("Welcome", "<b>x</b>"); got != "<!-- pocket-core-sso --><b>x</b><!-- /pocket-core-sso -->\nWelcome" {
		t.Fatalf("add %q", got)
	}
	if got := withButton(withButton("Welcome", "<b>x</b>"), "<b>y</b>"); got != "<!-- pocket-core-sso --><b>y</b><!-- /pocket-core-sso -->\nWelcome" {
		t.Fatalf("replace %q", got)
	}
	if got := withButton(withButton("Welcome", "<b>x</b>"), ""); got != "Welcome" {
		t.Fatalf("remove %q", got)
	}
}
