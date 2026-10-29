package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "\ufeff# комментарий\n" +
		"TG_BOT_TEST_NEW=\"значение с пробелами\"\n" +
		"TG_BOT_TEST_SINGLE='single'\n" +
		"export TG_BOT_TEST_EXPORT=exported\n" +
		"TG_BOT_TEST_KEEP=from-file\n" +
		"TG_BOT_TEST_EMPTY=\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TG_BOT_TEST_KEEP", "from-env")
	for _, key := range []string{"TG_BOT_TEST_NEW", "TG_BOT_TEST_SINGLE", "TG_BOT_TEST_EXPORT", "TG_BOT_TEST_EMPTY"} {
		os.Unsetenv(key)
		t.Cleanup(func() { os.Unsetenv(key) })
	}

	loadDotEnv(path)

	if got := os.Getenv("TG_BOT_TEST_NEW"); got != "значение с пробелами" {
		t.Errorf("TG_BOT_TEST_NEW=%q", got)
	}
	if got := os.Getenv("TG_BOT_TEST_SINGLE"); got != "single" {
		t.Errorf("TG_BOT_TEST_SINGLE=%q", got)
	}
	if got := os.Getenv("TG_BOT_TEST_EXPORT"); got != "exported" {
		t.Errorf("TG_BOT_TEST_EXPORT=%q", got)
	}
	if got := os.Getenv("TG_BOT_TEST_KEEP"); got != "from-env" {
		t.Errorf("переменная окружения перезаписана: %q", got)
	}
	if got, ok := os.LookupEnv("TG_BOT_TEST_EMPTY"); !ok || got != "" {
		t.Errorf("пустое значение не загружено: %q, %v", got, ok)
	}
}
