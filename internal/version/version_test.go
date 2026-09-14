package version

import "testing"

// TestVersionDefaultsToDevelopment proves the zero-value default an
// un-ldflags'd build reports - never empty, never a stale hardcoded
// release version left behind from a previous edit.
func TestVersionDefaultsToDevelopment(t *testing.T) {
	if Version != "development" {
		t.Fatalf("Version = %q, want the un-overridden default %q", Version, "development")
	}
}
