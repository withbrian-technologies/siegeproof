package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/withbrian-technologies/siegeproof/internal/mcp"
)

func TestNewProducesDeterministicSortedJSON(t *testing.T) {
	result := mcp.DiscoveryResult{
		ServerInfo: mcp.ServerInfo{Name: "server", Version: "1", ProtocolVersion: "2025-06-18"},
		Tools: []mcp.ToolSummary{
			{Name: "z", InputSchema: []byte(`{"b":2,"a":1}`)},
			{Name: "a", InputSchema: []byte(`{"type":"object"}`)},
		},
		Resources: []mcp.ResourceSummary{{Name: "resource"}},
		Prompts:   []mcp.PromptSummary{{Name: "prompt"}},
	}
	now := time.Date(2026, 9, 19, 13, 0, 0, 0, time.UTC)
	first, err := Marshal(New(result, "stdio", "0.1.0", now))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Marshal(New(result, "stdio", "0.1.0", now))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("report was not deterministic:\n%s\n%s", first, second)
	}
	if !strings.Contains(string(first), `"name": "a"`) || strings.Index(string(first), `"name": "a"`) > strings.Index(string(first), `"name": "z"`) {
		t.Fatalf("tools were not sorted: %s", first)
	}
	if !strings.Contains(string(first), "\"input_schema\": {\n        \"a\": 1,\n        \"b\": 2\n      }") {
		t.Fatalf("JSON fields were not normalized: %s", first)
	}
}

func TestWriteAtomicRejectsMissingParentWithoutChangingExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := New(mcp.DiscoveryResult{
		ServerInfo: mcp.ServerInfo{Name: "server", Version: "1", ProtocolVersion: "p"},
	}, "stdio", "v", time.Unix(0, 0))
	err := WriteAtomic(filepath.Join(dir, "missing", "report.json"), r)
	if err == nil {
		t.Fatal("expected missing parent error")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "old" {
		t.Fatalf("existing report changed: %q", got)
	}
}

func TestParseRejectsMalformedReport(t *testing.T) {
	if _, err := Parse([]byte(`{"schema":"siegeproof.discovery/v0"}`)); err == nil {
		t.Fatal("expected schema validation error")
	}
	if _, err := Parse([]byte(`{"schema":"siegeproof.discovery/v1","tool":{"name":"siegeproof","version":"v"},"run_timestamp":"bad","target_transport":"stdio","server":{"name":"s","version":"v","protocol_version":"p"},"tools":[],"resources":[],"prompts":[],"counts":{"tools":0,"resources":0,"prompts":0},"complete":true}`)); err == nil {
		t.Fatal("expected timestamp validation error")
	}
}

func TestWriteAtomicRejectsAbsoluteAndSymlinkPaths(t *testing.T) {
	dir := t.TempDir()
	r := New(mcp.DiscoveryResult{
		ServerInfo: mcp.ServerInfo{Name: "server", Version: "1", ProtocolVersion: "p"},
	}, "stdio", "v", time.Unix(0, 0))
	if err := WriteAtomic(filepath.Join(dir, "report.json"), r); err == nil {
		t.Fatal("expected absolute path rejection")
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(filepath.Join(dir, "report.json"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := WriteAtomic(link, r); err == nil {
		t.Fatal("expected symlink rejection")
	}
}
