package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Config — настройки бота, читаются из переменных окружения.
type Config struct {
	Token        string         // BOT_TOKEN, обязателен
	APIBase      string         // TELEGRAM_API_BASE, по умолчанию https://api.telegram.org
	LocalAPI     bool           // LOCAL_BOT_API=1 — поднимать лимит отправки до 2 ГБ
	YTDLP        string         // YTDLP_PATH
	FFmpeg       string         // FFMPEG_PATH (необязательно, если ffmpeg уже в PATH)
	WorkDir      string         // WORK_DIR для временных файлов
	MaxUploadMB  int64          // MAX_UPLOAD_MB
	Concurrency  int            // CONCURRENCY — сколько видео качать одновременно
	CookiesFile  string         // COOKIES_FILE — cookies.txt для закрытых видео
	Proxy        string         // YTDLP_PROXY
	AllowedUsers map[int64]bool // ALLOWED_USERS — список ID через запятую, пусто = для всех
	JobTimeout   time.Duration  // JOB_TIMEOUT_MIN
}

func LoadConfig() (*Config, error) {
	uploadLimitSet := strings.TrimSpace(os.Getenv("MAX_UPLOAD_MB")) != ""
	cfg := &Config{
		Token:        strings.TrimSpace(os.Getenv("BOT_TOKEN")),
		APIBase:      envString("TELEGRAM_API_BASE", "https://api.telegram.org"),
		LocalAPI:     envBool("LOCAL_BOT_API"),
		YTDLP:        envString("YTDLP_PATH", "yt-dlp"),
		FFmpeg:       strings.TrimSpace(os.Getenv("FFMPEG_PATH")),
		WorkDir:      envString("WORK_DIR", filepath.Join(os.TempDir(), "tg-video-bot")),
		MaxUploadMB:  envInt64("MAX_UPLOAD_MB", 50),
		Concurrency:  int(envInt64("CONCURRENCY", 2)),
		CookiesFile:  strings.TrimSpace(os.Getenv("COOKIES_FILE")),
		Proxy:        strings.TrimSpace(os.Getenv("YTDLP_PROXY")),
		AllowedUsers: parseUserIDs(os.Getenv("ALLOWED_USERS")),
		JobTimeout:   time.Duration(envInt64("JOB_TIMEOUT_MIN", 20)) * time.Minute,
	}

	if cfg.Token == "" {
		return nil, fmt.Errorf("BOT_TOKEN не задан")
	}
	cfg.APIBase = strings.TrimRight(cfg.APIBase, "/")
	if cfg.JobTimeout <= 0 {
		cfg.JobTimeout = 20 * time.Minute
	}
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	if cfg.MaxUploadMB < 1 {
		cfg.MaxUploadMB = 50
	}
	if cfg.LocalAPI && !uploadLimitSet {
		// Локальный Bot API умеет отдавать файлы до 2000 МБ.
		cfg.MaxUploadMB = 2000
	}
	if cfg.APIBase != "https://api.telegram.org" && !strings.HasPrefix(cfg.APIBase, "https://api.telegram.org/") {
		// Свой адрес Bot API почти всегда означает локальный сервер.
		cfg.LocalAPI = true
	}

	return cfg, nil
}

func (c *Config) logSummary() {
	users := "все"
	if len(c.AllowedUsers) > 0 {
		ids := make([]string, 0, len(c.AllowedUsers))
		for id := range c.AllowedUsers {
			ids = append(ids, strconv.FormatInt(id, 10))
		}
		sort.Strings(ids)
		users = strings.Join(ids, ", ")
	}
	log.Printf("конфиг: api=%s local=%v yt-dlp=%q ffmpeg=%q workdir=%s лимит=%d МБ потоков=%d доступ=%s",
		c.APIBase, c.LocalAPI, c.YTDLP, c.FFmpeg, c.WorkDir, c.MaxUploadMB, c.Concurrency, users)
}

func envString(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt64(key string, def int64) int64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		log.Printf("переменная %s=%q не число, использую %d", key, raw, def)
		return def
	}
	return v
}

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func parseUserIDs(raw string) map[int64]bool {
	ids := make(map[int64]bool)
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' '
	}) {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil {
			log.Printf("ALLOWED_USERS: пропускаю %q — не число", part)
			continue
		}
		ids[id] = true
	}
	return ids
}
