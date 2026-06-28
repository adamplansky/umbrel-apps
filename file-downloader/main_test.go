package main

import (
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
