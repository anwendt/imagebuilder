package revision_test

import (
	"strings"
	"testing"

	"github.com/anwendt/imagebuilder/pkg/controller/revision"
)

func TestResourceNameEmptyRevisionPreservesCompatibility(t *testing.T) {
	if got := revision.ResourceName("ubuntu-build", ""); got != "ubuntu-build" {
		t.Fatalf("ResourceName = %q, want ubuntu-build", got)
	}
}

func TestResourceNameRevisionIsStableAndDistinct(t *testing.T) {
	first := revision.ResourceName("ubuntu-build", "2026-08-12.1")
	if first != revision.ResourceName("ubuntu-build", "2026-08-12.1") {
		t.Fatal("same revision produced different names")
	}
	if first == revision.ResourceName("ubuntu-build", "2026-08-12.2") {
		t.Fatal("different revisions produced the same name")
	}
	if len(first) > 63 {
		t.Fatalf("resource name length = %d, want <= 63", len(first))
	}
}

func TestResourceNameTruncatesLongBase(t *testing.T) {
	got := revision.ResourceName(strings.Repeat("a", 63), "v2")
	if len(got) > 63 {
		t.Fatalf("resource name length = %d, want <= 63", len(got))
	}
}

func TestBuildIDIncludesRevision(t *testing.T) {
	if got := revision.BuildID("uid-123", ""); got != "uid-123" {
		t.Fatalf("BuildID without revision = %q", got)
	}
	if revision.BuildID("uid-123", "v1") == revision.BuildID("uid-123", "v2") {
		t.Fatal("different revisions produced the same build ID")
	}
}
