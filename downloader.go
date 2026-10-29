package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Progress — текущее состояние скачивания.
type Progress struct {
	Percent float64
	Speed   string
	ETA     string
	Total   string
	Stage   string
	Quality string
}

// Result — готовый к отправке файл.
type Result struct {
	Path     string
	Title    string
	Uploader string
	Duration int
	Width    int
	Height   int
	Size     int64
	URL      string

	dir string
}

// Cleanup удаляет временный каталог с видео и метаданными.
func (r *Result) Cleanup() {
	if r != nil && r.dir != "" {
		if err := os.RemoveAll(r.dir); err != nil {
			log.Printf("очистка %s: %v", r.dir, err)
		}
	}
}

// TooBigError означает, что даже минимальное качество не влезло в лимит.
type TooBigError struct {
	Size  int64
	Limit int64
}

func (e *TooBigError) Error() string {
	return fmt.Sprintf("файл %s больше лимита %s", humanSize(e.Size), humanSize(e.Limit))
}

type Downloader struct {
	bin       string
	ffmpeg    string
	workDir   string
	cookies   string
	proxy     string
	maxBytes  int64
	maxHeight int
	version   string
}

func NewDownloader(cfg *Config) *Downloader {
	return &Downloader{
		bin:       cfg.YTDLP,
		ffmpeg:    cfg.FFmpeg,
		workDir:   cfg.WorkDir,
		cookies:   cfg.CookiesFile,
		proxy:     cfg.Proxy,
		maxBytes:  cfg.MaxUploadMB * 1024 * 1024,
		maxHeight: int(envInt64("MAX_HEIGHT", 1080)),
	}
}

// Check проверяет, что yt-dlp установлен, а WORK_DIR доступен для записи.
func (d *Downloader) Check(ctx context.Context) error {
	if err := os.MkdirAll(d.workDir, 0o755); err != nil {
		return fmt.Errorf("не удалось создать WORK_DIR %s: %w", d.workDir, err)
	}
	probe, err := os.CreateTemp(d.workDir, ".write-check-*")
	if err != nil {
		return fmt.Errorf("нет прав на запись в WORK_DIR %s: %w", d.workDir, err)
	}
	probeName := probe.Name()
	probe.Close()
	_ = os.Remove(probeName)

	if d.cookies != "" {
		f, err := os.Open(d.cookies)
		if err != nil {
			return fmt.Errorf("не удалось прочитать COOKIES_FILE %s: %w", d.cookies, err)
		}
		f.Close()
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, d.bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("yt-dlp не запускается (%s): %w. Установка: https://github.com/yt-dlp/yt-dlp#installation",
			strings.TrimSpace(string(out)), err)
	}
	d.version = strings.TrimSpace(string(out))

	if d.ffmpeg == "" {
		if _, err := exec.LookPath("ffmpeg"); err != nil {
			log.Printf("внимание: ffmpeg не найден в PATH — раздельные дорожки видео/звука не склеятся")
		}
	}
	return nil
}

func (d *Downloader) Version() string { return d.version }

// CleanupStale удаляет временные каталоги прошлых запусков, которые остались
// после аварийного завершения.
func (d *Downloader) CleanupStale(maxAge time.Duration) {
	entries, err := os.ReadDir(d.workDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "job-") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		path := filepath.Join(d.workDir, e.Name())
		if err := os.RemoveAll(path); err == nil {
			log.Printf("удалён старый временный каталог %s", path)
		}
	}
}

// Download скачивает видео, понижая качество, пока файл не влезет в лимит отправки.
func (d *Downloader) Download(ctx context.Context, rawURL string, onProgress func(Progress)) (*Result, error) {
	if err := os.MkdirAll(d.workDir, 0o755); err != nil {
		return nil, fmt.Errorf("не удалось создать каталог %s: %w", d.workDir, err)
	}
	dir, err := os.MkdirTemp(d.workDir, "job-")
	if err != nil {
		return nil, fmt.Errorf("не удалось создать временный каталог: %w", err)
	}

	attempts := d.attempts()
	var lastSize int64
	for i, attempt := range attempts {
		path, info, err := d.run(ctx, rawURL, dir, attempt, onProgress)
		if err != nil {
			// При --max-filesize yt-dlp может молча пропустить слишком большой
			// формат и завершиться без файла — это повод попробовать ниже.
			tooBigForAttempt := isFilesizeAbort(err) ||
				(errors.Is(err, errNoMediaFile) && attempt.label != "worst")
			if ctx.Err() != nil || !tooBigForAttempt {
				os.RemoveAll(dir)
				return nil, err
			}
			log.Printf("на %s файл не влез в лимит, пробую ниже качеством", attempt.label)
			removeMediaFiles(dir)
			continue
		}

		size := fileSize(path)
		if size <= d.maxBytes {
			res := &Result{
				Path:     path,
				Title:    info.Title,
				Uploader: firstNonEmpty(info.Uploader, info.Channel),
				Duration: int(info.Duration),
				Width:    info.Width,
				Height:   info.Height,
				Size:     size,
				URL:      firstNonEmpty(info.WebpageURL, rawURL),
				dir:      dir,
			}
			if res.Title == "" {
				res.Title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			}
			return res, nil
		}

		lastSize = size
		log.Printf("файл %s не влез в %s, пробую ниже качеством (%d/%d)", humanSize(size), humanSize(d.maxBytes), i+1, len(attempts))
		removeMediaFiles(dir)
	}

	os.RemoveAll(dir)
	if lastSize > 0 {
		return nil, &TooBigError{Size: lastSize, Limit: d.maxBytes}
	}
	return nil, fmt.Errorf("не удалось получить видео")
}

type attempt struct {
	label  string
	format string
	sort   string
}

// attempts — лестница качеств: сверху вниз, последняя попытка — самое низкое качество.
func (d *Downloader) attempts() []attempt {
	var out []attempt
	if d.maxHeight <= 0 {
		out = append(out, attempt{
			label:  "best",
			format: "bv*[ext=mp4]+ba[ext=m4a]/bv*+ba/b",
			sort:   "res,codec:h264",
		})
	}
	for _, h := range []int{2160, 1440, 1080, 720, 480, 360} {
		if d.maxHeight > 0 && h > d.maxHeight {
			continue
		}
		out = append(out, attempt{
			label: fmt.Sprintf("%dp", h),
			format: fmt.Sprintf(
				"bv*[ext=mp4][height<=%d]+ba[ext=m4a]/b[ext=mp4][height<=%d]/bv*[height<=%d]+ba/b[height<=%d]/bv*+ba/b",
				h, h, h, h,
			),
			sort: fmt.Sprintf("res:%d,codec:h264", h),
		})
	}
	out = append(out, attempt{label: "worst", format: "wv*+wa/w", sort: "res:240"})
	return out
}

func (d *Downloader) run(ctx context.Context, rawURL, dir string, a attempt, onProgress func(Progress)) (string, *mediaInfo, error) {
	// yt-dlp перезаписывает cookie-файл при выходе, поэтому работаем с копией:
	// оригинал может быть смонтирован только для чтения, а параллельные
	// загрузки не будут мешать друг другу.
	cookies := d.cookies
	if cookies != "" {
		cookies = filepath.Join(dir, ".cookies.txt")
		if err := copyFile(d.cookies, cookies); err != nil {
			return "", nil, fmt.Errorf("не удалось подготовить COOKIES_FILE %s: %w", d.cookies, err)
		}
	}

	args := []string{
		"--no-warnings",
		"--no-color",
		"--newline",
		"--no-playlist",
		"--no-mtime",
		"--retries", "3",
		"--fragment-retries", "3",
		"--socket-timeout", "20",
		"--concurrent-fragments", "4",
		"--write-info-json",
		"--trim-filenames", "120",
		"--merge-output-format", "mp4",
		"-o", filepath.Join(dir, "%(title).100B [%(id)s].%(ext)s"),
		"-f", a.format,
		"-S", a.sort,
	}
	// На промежуточных попытках имеет смысл оборвать заведомо большой файл,
	// если yt-dlp знает его размер.
	if a.label != "worst" {
		args = append(args, "--max-filesize", strconv.FormatInt(d.maxBytes, 10))
	}
	if cookies != "" {
		args = append(args, "--cookies", cookies)
	}
	if d.proxy != "" {
		args = append(args, "--proxy", d.proxy)
	}
	if d.ffmpeg != "" {
		args = append(args, "--ffmpeg-location", d.ffmpeg)
	}
	args = append(args, "--", rawURL)

	cmd := exec.CommandContext(ctx, d.bin, args...)
	cmd.Env = os.Environ()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", nil, fmt.Errorf("stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", nil, fmt.Errorf("stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("не удалось запустить yt-dlp: %w", err)
	}
	if onProgress != nil {
		onProgress(Progress{Stage: "download", Quality: a.label})
	}

	var tail lineBuffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		scanLines(stdout, func(line string) {
			if p, ok := parseProgress(line); ok {
				p.Quality = a.label
				if onProgress != nil {
					onProgress(p)
				}
			}
			if isErrorLine(line) {
				tail.add(line)
			}
		})
	}()
	go func() {
		defer wg.Done()
		scanLines(stderr, func(line string) {
			if strings.TrimSpace(line) != "" {
				tail.add(line)
			}
		})
	}()

	waitErr := cmd.Wait()
	wg.Wait()

	if ctx.Err() != nil {
		return "", nil, fmt.Errorf("скачивание прервано: %w", ctx.Err())
	}
	if waitErr != nil {
		if msg := tail.lastError(); isFilesizeAbortText(msg) {
			return "", nil, &filesizeAbortError{msg: msg}
		}
		msg := tail.lastError()
		if msg == "" {
			msg = tail.tail(5)
		}
		if msg == "" {
			msg = waitErr.Error()
		}
		return "", nil, fmt.Errorf("yt-dlp: %s", msg)
	}

	path := findMediaFile(dir)
	if path == "" {
		return "", nil, fmt.Errorf("%w: возможно, по ссылке нет видео или размер превысил лимит", errNoMediaFile)
	}
	return path, readInfo(dir), nil
}

var errNoMediaFile = errors.New("yt-dlp завершился без файла")

type filesizeAbortError struct{ msg string }

func (e *filesizeAbortError) Error() string { return e.msg }

func isFilesizeAbort(err error) bool {
	var e *filesizeAbortError
	return errors.As(err, &e)
}

func isFilesizeAbortText(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "larger than max-filesize") ||
		strings.Contains(msg, "file is larger than") ||
		strings.Contains(msg, "filesize is larger")
}

type mediaInfo struct {
	Title      string  `json:"title"`
	Uploader   string  `json:"uploader"`
	Channel    string  `json:"channel"`
	Duration   float64 `json:"duration"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	WebpageURL string  `json:"webpage_url"`
}

func readInfo(dir string) *mediaInfo {
	info := &mediaInfo{}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.info.json"))
	if len(matches) == 0 {
		return info
	}
	data, err := os.ReadFile(matches[len(matches)-1])
	if err != nil {
		log.Printf("не удалось прочитать метаданные: %v", err)
		return info
	}
	if err := json.Unmarshal(data, info); err != nil {
		log.Printf("не удалось разобрать метаданные: %v", err)
		return &mediaInfo{}
	}
	return info
}

func findMediaFile(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var best string
	var bestSize int64
	for _, e := range entries {
		if e.IsDir() || isSidecar(strings.ToLower(e.Name())) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.Size() > bestSize {
			best = filepath.Join(dir, e.Name())
			bestSize = info.Size()
		}
	}
	return best
}

func isSidecar(name string) bool {
	for _, suffix := range []string{".json", ".part", ".ytdl", ".temp", ".jpg", ".jpeg", ".png", ".webp", ".vtt", ".srt", ".description", ".txt"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return strings.HasPrefix(name, ".")
}

func removeMediaFiles(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if strings.HasSuffix(name, ".info.json") {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}

var percentRe = regexp.MustCompile(`^\[download\]\s+([0-9]+(?:\.[0-9]+)?)%`)

func parseProgress(line string) (Progress, bool) {
	switch {
	case strings.HasPrefix(line, "[Merger]"):
		return Progress{Stage: "merge"}, true
	case strings.HasPrefix(line, "[FixupM3u8]"), strings.HasPrefix(line, "[VideoRemuxer]"), strings.HasPrefix(line, "[VideoConvertor]"), strings.HasPrefix(line, "[ExtractAudio]"):
		return Progress{Stage: "merge"}, true
	case strings.HasPrefix(line, "[download] Destination:"):
		return Progress{Stage: "download"}, true
	}

	m := percentRe.FindStringSubmatch(line)
	if m == nil {
		return Progress{}, false
	}
	pct, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return Progress{}, false
	}
	p := Progress{Percent: pct, Stage: "download"}
	fields := strings.Fields(line)
	for i, f := range fields {
		if i+1 >= len(fields) {
			break
		}
		switch f {
		case "at":
			if fields[i+1] != "Unknown" {
				p.Speed = fields[i+1]
			}
		case "ETA":
			if fields[i+1] != "Unknown" {
				p.ETA = fields[i+1]
			}
		case "of":
			next := fields[i+1]
			if next == "~" && i+2 < len(fields) {
				next = fields[i+2]
			}
			p.Total = next
		}
	}
	return p, true
}

func isErrorLine(line string) bool {
	return strings.HasPrefix(line, "ERROR:") || strings.HasPrefix(line, "WARNING:")
}

type lineBuffer struct {
	mu    sync.Mutex
	lines []string
}

func (b *lineBuffer) add(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, strings.TrimSpace(line))
	if len(b.lines) > 50 {
		b.lines = b.lines[len(b.lines)-50:]
	}
}

func (b *lineBuffer) tail(n int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.lines) == 0 {
		return ""
	}
	start := len(b.lines) - n
	if start < 0 {
		start = 0
	}
	return strings.Join(b.lines[start:], "\n")
}

func (b *lineBuffer) lastError() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := len(b.lines) - 1; i >= 0; i-- {
		line := b.lines[i]
		if strings.HasPrefix(line, "ERROR:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "ERROR:"))
		}
	}
	return ""
}

func scanLines(r io.Reader, fn func(string)) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		fn(strings.TrimRight(scanner.Text(), "\r"))
	}
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func humanSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d Б", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit && exp < 5; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func isTooBig(err error) bool {
	var e *TooBigError
	return errors.As(err, &e)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
