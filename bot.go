package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	maxURLsPerMessage = 3
	jobQueueSize      = 100
)

type Bot struct {
	cfg *Config
	tg  *Client
	dl  *Downloader

	jobs      chan job
	userLocks *userLocks
	wg        sync.WaitGroup
}

type job struct {
	chatID    int64
	threadID  int64
	userID    int64
	messageID int64
	urls      []string
}

func NewBot(cfg *Config, tg *Client, dl *Downloader) *Bot {
	return &Bot{
		cfg:       cfg,
		tg:        tg,
		dl:        dl,
		jobs:      make(chan job, jobQueueSize),
		userLocks: newUserLocks(),
	}
}

// Run запускает воркеров и long polling. Возвращается при отмене ctx.
func (b *Bot) Run(ctx context.Context) error {
	for i := 0; i < b.cfg.Concurrency; i++ {
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			b.worker(ctx)
		}()
	}

	log.Printf("бот запущен, жду ссылки")
	offset := int64(0)
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return nil
		}

		pollCtx, cancel := context.WithTimeout(ctx, 70*time.Second)
		updates, err := b.tg.GetUpdates(pollCtx, offset, 50)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("getUpdates: %v", err)
			sleepCtx(ctx, backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second

		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			msg := u.Message
			if msg == nil {
				msg = u.EditedMessage
			}
			if msg != nil {
				b.handle(msg)
			}
		}
	}
}

// Shutdown ждёт завершения текущих задач не дольше timeout.
func (b *Bot) Shutdown(timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		log.Printf("не все загрузки завершились за %s, выхожу", timeout)
	}
}

func (b *Bot) worker(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case j := <-b.jobs:
			b.process(j)
		}
	}
}

func (b *Bot) handle(msg *Message) {
	// Анонимный админ приходит от служебного GroupAnonymousBot (или без from),
	// но с sender_chat, совпадающим с чатом.
	anonymous := isAnonymousAdmin(msg)
	if !anonymous && (msg.From == nil || msg.From.IsBot) {
		return
	}

	text := strings.TrimSpace(firstNonEmpty(msg.Text, msg.Caption))
	if text == "" {
		return
	}

	if strings.HasPrefix(text, "/") {
		// /id доступен всем и всегда, остальные команды — только своим.
		if commandName(text) != "/id" && !b.allowed(senderID(msg)) {
			return
		}
		b.handleCommand(msg, text)
		return
	}

	if !b.allowed(senderID(msg)) {
		return
	}

	// В группах бот не реагирует на обычные сообщения — только на /v <ссылка>.
	if isGroupChat(msg.Chat.Type) {
		return
	}

	b.handleURLs(msg, text)
}

// handleURLs ставит ссылки из сообщения в очередь на скачивание.
func (b *Bot) handleURLs(msg *Message, text string) {
	urls := extractURLs(text)
	if len(urls) == 0 {
		b.notify(msg.Chat.ID, msg.MessageThreadID, "Пришлите ссылку на видео — скачаю и отправлю файлом.\n\nНапример: <code>https://youtu.be/dQw4w9WgXcQ</code>", msg.MessageID)
		return
	}
	b.enqueue(msg, urls)
}

func (b *Bot) enqueue(msg *Message, urls []string) {
	if len(urls) > maxURLsPerMessage {
		urls = urls[:maxURLsPerMessage]
		b.notify(msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("В сообщении больше %d ссылок — обработаю первые %d.", maxURLsPerMessage, maxURLsPerMessage), msg.MessageID)
	}

	uid := senderID(msg)
	if !b.userLocks.tryLock(uid) {
		b.notify(msg.Chat.ID, msg.MessageThreadID, "⏳ У вас уже есть активная загрузка. Дождитесь её окончания.", msg.MessageID)
		return
	}

	select {
	case b.jobs <- job{chatID: msg.Chat.ID, threadID: msg.MessageThreadID, userID: uid, messageID: msg.MessageID, urls: urls}:
	default:
		b.userLocks.unlock(uid)
		b.notify(msg.Chat.ID, msg.MessageThreadID, "🚦 Очередь переполнена, попробуйте через пару минут.", msg.MessageID)
	}
}

func (b *Bot) handleCommand(msg *Message, text string) {
	cmd := commandName(text)

	switch cmd {
	case "/start", "/help":
		b.notify(msg.Chat.ID, msg.MessageThreadID, helpText, msg.MessageID)
	case "/id":
		b.replyID(msg)
	case "/v":
		urls := extractURLs(commandArg(text))
		if len(urls) == 0 {
			b.notify(msg.Chat.ID, msg.MessageThreadID, "Пришлите ссылку после команды: <code>/v https://youtu.be/...</code>", msg.MessageID)
			return
		}
		b.enqueue(msg, urls)
	default:
		b.notify(msg.Chat.ID, msg.MessageThreadID, "Неизвестная команда. Наберите /help.", msg.MessageID)
	}
}

func isGroupChat(chatType string) bool {
	return chatType == "group" || chatType == "supergroup"
}

// commandName возвращает команду в нижнем регистре без упоминания бота.
func commandName(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return ""
	}
	cmd := strings.ToLower(fields[0])
	if i := strings.IndexByte(cmd, '@'); i > 0 {
		cmd = cmd[:i]
	}
	return cmd
}

// replyID показывает ID отправителя; работает независимо от ALLOWED_USERS.
func (b *Bot) replyID(msg *Message) {
	label := "Ваш Telegram ID"
	if isAnonymousAdmin(msg) {
		label = "ID группы (анонимный админ)"
	}
	b.notify(msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("%s: <code>%d</code>", label, senderID(msg)), msg.MessageID)
}

// isAnonymousAdmin — сообщение отправлено от имени группы (анонимный админ).
func isAnonymousAdmin(msg *Message) bool {
	return isGroupChat(msg.Chat.Type) && msg.SenderChat != nil && msg.SenderChat.ID == msg.Chat.ID
}

// senderID возвращает ID пользователя, а для анонимного админа — ID группы.
func senderID(msg *Message) int64 {
	if isAnonymousAdmin(msg) {
		return msg.SenderChat.ID
	}
	return msg.From.ID
}

// commandArg возвращает текст после первого слова команды.
func commandArg(text string) string {
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return ""
	}
	idx := strings.Index(text, fields[0])
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(text[idx+len(fields[0]):])
}

const helpText = `🎬 <b>Отправьте ссылку на видео — пришлю скачанный файл.</b>

Поддерживаются YouTube, X (Twitter), TikTok, Instagram, Vimeo, VK, Rutube, Reddit, Twitch и тысячи других сайтов — всё, что умеет yt-dlp.

• Можно прислать до 3 ссылок в одном сообщении.
• В группе — только по команде <code>/v ссылка</code>.
• Качество подбирается автоматически под лимит Telegram.
• /id — узнать свой Telegram ID.

Если видео не скачивается, возможно, оно приватное или требует входа.`

func (b *Bot) process(j job) {
	defer b.userLocks.unlock(j.userID)

	for _, u := range j.urls {
		b.processOne(j, u)
	}
}

func (b *Bot) processOne(j job, rawURL string) {
	log.Printf("загрузка chat=%d user=%d url=%s", j.chatID, j.userID, rawURL)

	ctx, cancel := context.WithTimeout(context.Background(), b.cfg.JobTimeout)
	defer cancel()

	status, err := b.tg.SendMessage(ctx, j.chatID, j.threadID, "⏳ Скачиваю видео…", j.messageID)
	if err != nil {
		log.Printf("не удалось отправить статус: %v", err)
		return
	}
	statusID := status.MessageID

	progressCh := make(chan Progress, 1)
	pdone := make(chan struct{})
	var pw sync.WaitGroup
	pw.Add(1)
	go func() {
		defer pw.Done()
		b.progressLoop(ctx, j.chatID, statusID, progressCh, pdone)
	}()

	res, dlErr := b.dl.Download(ctx, rawURL, func(p Progress) {
		select {
		case progressCh <- p:
		default:
		}
	})
	close(pdone)
	pw.Wait()

	if dlErr != nil {
		log.Printf("ошибка загрузки %s: %v", rawURL, dlErr)
		b.reportFailure(ctx, j.chatID, statusID, dlErr)
		return
	}
	defer res.Cleanup()

	limitBytes := b.cfg.MaxUploadMB * 1024 * 1024
	if res.Size > limitBytes {
		b.edit(j.chatID, statusID, fmt.Sprintf("⚠️ Видео весит %s, а лимит отправки — %s.", humanSize(res.Size), humanSize(limitBytes)))
		return
	}

	b.edit(j.chatID, statusID, "📤 Отправляю видео…")

	caption := b.caption(res)
	useLocalRef := b.cfg.LocalAPI && res.Size > 50*1024*1024

	_, err = b.tg.SendVideo(ctx, Video{
		ChatID:      j.chatID,
		ThreadID:    j.threadID,
		ReplyTo:     j.messageID,
		Path:        res.Path,
		Caption:     caption,
		Width:       res.Width,
		Height:      res.Height,
		Duration:    res.Duration,
		UseLocalRef: useLocalRef,
	})
	if err != nil {
		log.Printf("sendVideo не удался (%v), пробую отправить документом", err)
		sendCtx, sendCancel := context.WithTimeout(context.Background(), 15*time.Minute)
		_, docErr := b.tg.SendDocument(sendCtx, Document{
			ChatID:      j.chatID,
			ThreadID:    j.threadID,
			ReplyTo:     j.messageID,
			Path:        res.Path,
			Caption:     caption,
			UseLocalRef: useLocalRef,
		})
		sendCancel()
		if docErr != nil {
			log.Printf("sendDocument тоже не удался: %v", docErr)
			if isTelegramTooBig(err) || isTelegramTooBig(docErr) {
				b.edit(j.chatID, statusID, fmt.Sprintf("⚠️ Файл %s не принимается Telegram (лимит %s).\nУменьшите <code>MAX_UPLOAD_MB</code> или поднимите локальный Bot API.", humanSize(res.Size), humanSize(limitBytes)))
				return
			}
			b.edit(j.chatID, statusID, "❌ Не удалось отправить файл в Telegram: "+html.EscapeString(truncate(docErr.Error(), 300)))
			return
		}
	}

	log.Printf("отправлено chat=%d url=%s размер=%s", j.chatID, rawURL, humanSize(res.Size))
	b.edit(j.chatID, statusID, fmt.Sprintf("✅ Готово · %s", humanSize(res.Size)))
}

func (b *Bot) progressLoop(ctx context.Context, chatID, messageID int64, ch <-chan Progress, done <-chan struct{}) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	var last Progress
	pending := false
	for {
		select {
		case p := <-ch:
			last = mergeProgress(last, p)
			pending = true
		case <-ticker.C:
			if pending {
				b.edit(chatID, messageID, progressText(last))
				pending = false
			}
		case <-done:
			return
		case <-ctx.Done():
			return
		}
	}
}

func mergeProgress(old, p Progress) Progress {
	if p.Stage == "" {
		p.Stage = old.Stage
	}
	// Новая попытка скачивания в другом качестве — старый процент уже неактуален.
	if p.Quality != "" && old.Quality != "" && p.Quality != old.Quality {
		old = Progress{}
	}
	if p.Stage == "merge" {
		p.Percent = 0
		return p
	}
	if p.Percent == 0 && old.Stage == "download" {
		p.Percent = old.Percent
	}
	if p.Speed == "" {
		p.Speed = old.Speed
	}
	if p.ETA == "" {
		p.ETA = old.ETA
	}
	if p.Total == "" {
		p.Total = old.Total
	}
	if p.Quality == "" {
		p.Quality = old.Quality
	}
	return p
}

func progressText(p Progress) string {
	if p.Stage == "merge" {
		return "🔧 Склеиваю видео и звук…"
	}
	if p.Percent <= 0 {
		if p.Quality != "" && p.Quality != "best" && p.Quality != "worst" {
			return "⏳ Скачиваю видео… (до " + p.Quality + ")"
		}
		return "⏳ Скачиваю видео…"
	}
	line := fmt.Sprintf("⏳ Скачиваю видео… <b>%.0f%%</b>", p.Percent)
	details := make([]string, 0, 3)
	if p.Quality != "" && p.Quality != "best" && p.Quality != "worst" {
		details = append(details, "до "+p.Quality)
	}
	if p.Total != "" {
		details = append(details, "из "+p.Total)
	}
	if p.Speed != "" && !strings.HasPrefix(p.Speed, "Unknown") {
		details = append(details, p.Speed)
	}
	if p.ETA != "" {
		details = append(details, "осталось "+p.ETA)
	}
	if len(details) > 0 {
		line += "\n" + strings.Join(details, " · ")
	}
	return line
}

func (b *Bot) reportFailure(ctx context.Context, chatID, messageID int64, err error) {
	if isTooBig(err) {
		b.edit(chatID, messageID, fmt.Sprintf("⚠️ Видео слишком большое: даже минимальное качество не влезает в лимит %s.\n\nПопросите владельца бота увеличить <code>MAX_UPLOAD_MB</code> (для этого нужен локальный Bot API).", humanSize(b.cfg.MaxUploadMB*1024*1024)))
		return
	}
	if ctx.Err() == context.DeadlineExceeded {
		b.edit(chatID, messageID, fmt.Sprintf("⏱ Превышено время ожидания (%s). Попробуйте ссылку на видео покороче.", b.cfg.JobTimeout))
		return
	}
	msg := cleanError(err.Error())
	b.edit(chatID, messageID, "❌ Не удалось скачать видео.\n<code>"+html.EscapeString(truncate(msg, 600))+"</code>")
}

func (b *Bot) caption(res *Result) string {
	title := strings.TrimSpace(strings.ReplaceAll(res.Title, "\n", " "))
	var sb strings.Builder
	sb.WriteString("<b>")
	sb.WriteString(html.EscapeString(truncate(title, 700)))
	sb.WriteString("</b>")

	var meta []string
	if res.Uploader != "" {
		meta = append(meta, html.EscapeString(truncate(res.Uploader, 120)))
	}
	if res.Duration > 0 {
		meta = append(meta, formatDuration(res.Duration))
	}
	meta = append(meta, humanSize(res.Size))
	if len(meta) > 0 {
		sb.WriteString("\n")
		sb.WriteString(strings.Join(meta, " · "))
	}
	return sb.String()
}

func (b *Bot) allowed(userID int64) bool {
	return len(b.cfg.AllowedUsers) == 0 || b.cfg.AllowedUsers[userID]
}

func (b *Bot) notify(chatID, threadID int64, text string, replyTo int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := b.tg.SendMessage(ctx, chatID, threadID, text, replyTo); err != nil {
		log.Printf("sendMessage: %v", err)
	}
}

func (b *Bot) edit(chatID, messageID int64, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := b.tg.EditMessageText(ctx, chatID, messageID, text); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && strings.Contains(strings.ToLower(apiErr.Description), "not modified") {
			return
		}
		log.Printf("editMessageText: %v", err)
	}
}

func formatDuration(seconds int) string {
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func cleanError(msg string) string {
	msg = strings.TrimSpace(msg)
	msg = strings.ReplaceAll(msg, "\r", "")
	msg = strings.ReplaceAll(msg, "\n", " · ")
	msg = strings.ReplaceAll(msg, "\t", " ")
	for strings.Contains(msg, "  ") {
		msg = strings.ReplaceAll(msg, "  ", " ")
	}
	return truncate(msg, 600)
}

var (
	urlRe = regexp.MustCompile(`https?://[^\s<>"'«»]+`)
	// Ссылки без схемы на популярные видеохостинги.
	bareURLRe = regexp.MustCompile(`(?i)\b(?:www\.)?(?:youtube\.com|youtu\.be|x\.com|twitter\.com|tiktok\.com|instagram\.com|vimeo\.com|vk\.com|vkvideo\.ru|rutube\.ru|reddit\.com|twitch\.tv|dailymotion\.com|facebook\.com|ok\.ru)/[^\s<>"'«»]+`)
)

func extractURLs(text string) []string {
	var raw []string
	raw = append(raw, urlRe.FindAllString(text, -1)...)
	if len(raw) == 0 {
		for _, u := range bareURLRe.FindAllString(text, -1) {
			raw = append(raw, "https://"+u)
		}
	}

	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, u := range raw {
		u = strings.TrimRight(u, ".,;:!?)]}>")
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	return out
}

type userLocks struct {
	mu   sync.Mutex
	busy map[int64]bool
}

func newUserLocks() *userLocks {
	return &userLocks{busy: make(map[int64]bool)}
}

func (l *userLocks) tryLock(userID int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.busy[userID] {
		return false
	}
	l.busy[userID] = true
	return true
}

func (l *userLocks) unlock(userID int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.busy, userID)
}
