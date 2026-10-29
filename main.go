package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[bot] ")

	loadDotEnv(".env")

	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("конфигурация: %v", err)
	}
	cfg.logSummary()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dl := NewDownloader(cfg)
	if err := dl.Check(ctx); err != nil {
		log.Fatalf("проверка yt-dlp: %v", err)
	}
	log.Printf("yt-dlp %s", dl.Version())
	dl.CleanupStale(12 * time.Hour)

	tg := NewClient(cfg.Token, cfg.APIBase)
	meCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	me, err := tg.GetMe(meCtx)
	cancel()
	if err != nil {
		log.Fatalf("не удалось авторизоваться в Telegram: %v", err)
	}
	log.Printf("подключён как @%s (id %d)", me.Username, me.ID)

	bot := NewBot(cfg, tg, dl)
	if err := bot.Run(ctx); err != nil {
		log.Fatalf("бот остановлен с ошибкой: %v", err)
	}

	log.Printf("останавливаюсь, жду текущие загрузки")
	bot.Shutdown(20 * time.Second)
}
