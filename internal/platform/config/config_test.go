package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadLocalEnvStripsQuotes(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "# comment\nTD_TEST_PLAIN=a=b;c\nTD_TEST_QUOTED=\"x=1;y=2\"\nTD_TEST_SET=from-file\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TD_TEST_SET", "from-env")
	for _, k := range []string{"TD_TEST_PLAIN", "TD_TEST_QUOTED"} {
		t.Setenv(k, "")
	}
	loadLocalEnv(path)

	for k, want := range map[string]string{"TD_TEST_PLAIN": "a=b;c", "TD_TEST_QUOTED": "x=1;y=2", "TD_TEST_SET": "from-env"} {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}
