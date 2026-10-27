package builder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProvisionerJSONReadersSeeCompleteDocuments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	payload := map[string]string{"inline": strings.Repeat("x", 256<<10)}
	if err := writeProvisionerJSON(path, payload); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 40; i++ {
			if err := writeProvisionerJSON(path, payload); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]string
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("reader observed incomplete document: %v", err)
		}
		if decoded["inline"] != payload["inline"] {
			t.Fatal("reader observed different content")
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			return
		default:
		}
	}
}
