package conf_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/quinnovator/contactd/internal/tui/conf"
)

func TestSaveHelpersPreservesSettings(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "chat.toml")
	if err := os.WriteFile(path, []byte("theme = \"dark\"\n[glyph]\nwire = \"<>\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := conf.SaveHelpers(path, "off"); err != nil {
		t.Fatal(err)
	}
	c, warnings, err := conf.LoadChat(path)
	if err != nil || len(warnings) > 0 || c.Helpers != "off" || c.Theme != "dark" || c.Glyph["wire"] != "<>" {
		t.Fatalf("settings: %+v %v %v", c, warnings, err)
	}
}
