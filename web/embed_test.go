package web

import (
	"io/fs"
	"testing"
)

func TestAssetsEmbedded(t *testing.T) {
	for _, name := range []string{"index.html", "style.css", "app.js"} {
		if _, err := fs.Stat(FS, name); err != nil {
			t.Fatalf("embedded asset %s missing: %v", name, err)
		}
	}
}
