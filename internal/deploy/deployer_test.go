package deploy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yireo/go-static-deploy/internal/magento"
)

func TestResolveVersion(t *testing.T) {
	d := &Deployer{opts: Options{ContentVersion: "12345"}}
	if got := d.ResolveVersion(); got != "12345" {
		t.Fatalf("ResolveVersion() = %q, want 12345", got)
	}
	d = &Deployer{}
	if got := d.ResolveVersion(); got == "" {
		t.Fatal("ResolveVersion() returned an empty timestamp")
	}
}

func TestWriteVersion(t *testing.T) {
	root := t.TempDir()
	d := &Deployer{env: &magento.Env{Root: root}}
	dir := filepath.Join(root, "pub", "static")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "deployed_version.txt")
	if err := os.WriteFile(file, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := d.WriteVersion("new"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("deployed_version.txt = %q, want new", data)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected only deployed_version.txt, got %d entries", len(entries))
	}
}
