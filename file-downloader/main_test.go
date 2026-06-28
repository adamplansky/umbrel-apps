package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMovieTargetPath(t *testing.T) {
	got := movieTargetPath("Andel Pane 2 (2016).mkv")
	want := filepath.Join("Movies", "Andel Pane 2 (2016)", "Andel Pane 2 (2016).mkv")
	if got != want {
		t.Fatalf("movieTargetPath() = %q, want %q", got, want)
	}
}

func TestSeriesTargetPath(t *testing.T) {
	got := seriesTargetPath(SeriesDownloadItem{
		URL:      "https://example.invalid/file",
		Filename: "source-name.mkv",
		ShowName: "Nazev serialu",
		ShowYear: "2020",
		Season:   1,
		Episode:  2,
	})
	want := filepath.Join("Shows", "Nazev serialu (2020)", "Season 01", "Nazev serialu S01E02.mkv")
	if got != want {
		t.Fatalf("seriesTargetPath() = %q, want %q", got, want)
	}
}

func TestResolveOutputPathRejectsTraversal(t *testing.T) {
	if _, err := resolveOutputPath(t.TempDir(), filepath.Join("Shows", "..", "..", "escape.mkv")); err == nil {
		t.Fatal("resolveOutputPath() accepted traversal path")
	}
}

func TestLoadEnvFileOverridesEmptyExistingEnv(t *testing.T) {
	t.Setenv("WEBSHARE_USERNAME", "")
	path := filepath.Join(t.TempDir(), "webshare.env")
	if err := os.WriteFile(path, []byte("WEBSHARE_USERNAME=\"user\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := loadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("WEBSHARE_USERNAME"); got != "user" {
		t.Fatalf("WEBSHARE_USERNAME = %q, want user", got)
	}
}
