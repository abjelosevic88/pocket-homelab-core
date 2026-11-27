package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The catalog is a reviewed list of what an AI assistant may call in each app.
// These two values pin all of it: every tool, in listing order, with everything
// the builder sets on it. A change to the catalog changes them, so it shows up
// in review as a change to this file too; moving the declarations around must not.
//
// When the catalog is changed on purpose: run
//
//	MCP_CATALOG_DUMP=/tmp/catalog.txt go test ./internal/server/ -run TestMCPAppCatalogGolden
//
// before and after, diff the two files, and put the new values here.
const (
	mcpAppCatalogGoldenTools  = 727
	mcpAppCatalogGoldenSHA256 = "239c1e5c159c8a6d7b6d98d72de5d87096ea9cc278fec48a1b793fc16c12c54e"
)

// mcpAppCatalogDump writes the catalog as text, one block per tool in listing
// order. encoding/json sorts map keys, so the same catalog always gives the same text.
func mcpAppCatalogDump(t *testing.T, catalog []*mcpAppTool) string {
	t.Helper()
	asJSON := func(name string, v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return string(raw)
	}
	var out strings.Builder
	for i, op := range catalog {
		// The validator is compiled from Schema when the tool is added. A tool whose
		// Schema is edited afterwards and not compiled again advertises one schema
		// and checks another: "stale". (Compared through a fresh compile, so the
		// text does not depend on how the schema library writes its own types.)
		validator := "none"
		if op.resolved != nil {
			validator = "current"
			fresh := compileMCPAppTool(&mcpAppTool{Name: op.Name, Schema: op.Schema})
			if !bytes.Equal([]byte(asJSON(op.Name, op.resolved.Schema())), []byte(asJSON(op.Name, fresh.resolved.Schema()))) {
				validator = "stale"
			}
		}
		fmt.Fprintf(&out, "== %d %s\n", i+1, op.Name)
		fmt.Fprintf(&out, "app: %s\n", op.App)
		fmt.Fprintf(&out, "description: %s\n", strconv.Quote(op.Description))
		fmt.Fprintf(&out, "method: %s\n", op.Method)
		fmt.Fprintf(&out, "path: %s\n", op.Path)
		fmt.Fprintf(&out, "kind: %s\n", op.Kind)
		fmt.Fprintf(&out, "schema: %s\n", asJSON(op.Name, op.Schema))
		fmt.Fprintf(&out, "validator: %s\n", validator)
		fmt.Fprintf(&out, "text_response: %t\n", op.TextResponse)
		fmt.Fprintf(&out, "file_response: %t\n", op.FileResponse)
		fmt.Fprintf(&out, "merge: %t\n", op.Merge)
		fmt.Fprintf(&out, "within: %s\n", strconv.Quote(op.Within))
		fmt.Fprintf(&out, "query: %s\n", asJSON(op.Name, op.Query))
		fmt.Fprintf(&out, "body: %s\n", asJSON(op.Name, op.Body))
		fmt.Fprintf(&out, "omit: %s\n", asJSON(op.Name, op.Omit))
	}
	return out.String()
}

func TestMCPAppCatalogGolden(t *testing.T) {
	dump := mcpAppCatalogDump(t, mcpAppCatalog)
	if path := os.Getenv("MCP_CATALOG_DUMP"); path != "" {
		if err := os.WriteFile(path, []byte(dump), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// built again from nothing, it is the same catalog: no tool depends on what an earlier build left behind
	if again := mcpAppCatalogDump(t, buildMCPAppCatalog()); again != dump {
		t.Error("buildMCPAppCatalog() does not give the same catalog twice")
	}
	sum := sha256.Sum256([]byte(dump))
	if got := hex.EncodeToString(sum[:]); len(mcpAppCatalog) != mcpAppCatalogGoldenTools || got != mcpAppCatalogGoldenSHA256 {
		t.Errorf("the MCP app catalog changed: %d tools, sha256 %s (pinned: %d tools, sha256 %s).\n"+
			"If the change is meant and reviewed, put the new values in this file; MCP_CATALOG_DUMP=<file> writes the full text to diff.",
			len(mcpAppCatalog), got, mcpAppCatalogGoldenTools, mcpAppCatalogGoldenSHA256)
	}
}
