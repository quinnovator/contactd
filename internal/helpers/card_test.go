package helpers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func goldenJSON(t *testing.T, name string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_A2A_GOLDENS") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(raw) {
		t.Fatalf("golden %s changed:\n%s", name, raw)
	}
}

func TestAgentCardGoldens(t *testing.T) {
	t.Parallel()
	cfg := DefaultConfig()
	goldenJSON(t, "card.golden.json", Card(cfg))
	cfg.Auth = "off"
	goldenJSON(t, "card-public.golden.json", Card(cfg))
}

func TestPrivateTokenFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "a2a.token")
	token, err := LoadToken(path, "")
	if err != nil {
		t.Fatal(err)
	}
	again, err := LoadToken(path, "")
	if err != nil || again != token || len(token) < 40 {
		t.Fatalf("stable token: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("token mode: %v %v", st, err)
	}
	explicit, err := LoadToken(path, "explicit")
	if err != nil || explicit != "explicit" {
		t.Fatal("explicit token ignored")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadToken(path, ""); err == nil {
		t.Fatal("accepted a token file readable by other users")
	}
}
