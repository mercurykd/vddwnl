package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAttemptsLadder(t *testing.T) {
	d := &Downloader{maxHeight: 1080}
	var labels []string
	for _, a := range d.attempts() {
		labels = append(labels, a.label)
	}
	want := []string{"1080p", "720p", "480p", "360p", "worst"}
	if len(labels) != len(want) {
		t.Fatalf("получено %v, ожидалось %v", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Errorf("шаг %d: получено %q, ожидалось %q", i, labels[i], want[i])
		}
	}

	d = &Downloader{maxHeight: 0}
	labels = labels[:0]
	for _, a := range d.attempts() {
		labels = append(labels, a.label)
	}
	if labels[0] != "best" {
		t.Errorf("при MAX_HEIGHT=0 первая попытка должна быть best, получено %v", labels)
	}
	if labels[len(labels)-1] != "worst" {
		t.Errorf("последняя попытка должна быть worst, получено %v", labels)
	}
}

func TestFindMediaFile(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"video.mp4":       "0123456789",
		"video.info.json": "{}",
		"video.webp":      "thumb",
		"video.part":      "partial",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := findMediaFile(dir)
	if filepath.Base(got) != "video.mp4" {
		t.Fatalf("получено %q, ожидался video.mp4", got)
	}
}

// TestDownloaderLive скачивает реальное видео. Запуск:
//
//	TEST_URL=https://youtu.be/... go test -run TestDownloaderLive -v
func TestDownloaderLive(t *testing.T) {
	rawURL := os.Getenv("TEST_URL")
	if rawURL == "" {
		t.Skip("TEST_URL не задан")
	}
	cfg := &Config{
		YTDLP:       envString("YTDLP_PATH", "yt-dlp"),
		FFmpeg:      os.Getenv("FFMPEG_PATH"),
		WorkDir:     t.TempDir(),
		MaxUploadMB: 50,
	}
	dl := NewDownloader(cfg)
	if err := dl.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Logf("yt-dlp %s", dl.Version())

	var last Progress
	res, err := dl.Download(context.Background(), rawURL, func(p Progress) {
		if p.Percent > last.Percent {
			last = p
		}
	})
	if err != nil {
		t.Fatalf("скачивание: %v", err)
	}
	defer res.Cleanup()

	if fileSize(res.Path) == 0 {
		t.Fatal("получен пустой файл")
	}
	t.Logf("файл=%s размер=%s заголовок=%q качество=%q", res.Path, humanSize(res.Size), res.Title, last.Quality)
}
