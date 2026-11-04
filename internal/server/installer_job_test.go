package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// The first snapshot of a job (before the agent logged anything) must carry
// "log": [] — the UI counts its lines and crashed on null.
func TestInstallJobSnapshotEmptyLog(t *testing.T) {
	b, _ := json.Marshal((&installJob{ID: "x", Kind: "uninstall", State: "running"}).snapshot())
	if !strings.Contains(string(b), `"log":[]`) {
		t.Fatalf("%s", b)
	}
}
