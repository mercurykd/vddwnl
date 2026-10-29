package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Client — минимальный клиент Telegram Bot API без сторонних библиотек.
type Client struct {
	token string
	base  string
	http  *http.Client
}

func NewClient(token, base string) *Client {
	return &Client{
		token: token,
		base:  strings.TrimRight(base, "/"),
		// Таймауты задаются через контекст: загрузка видео может идти долго.
		http: &http.Client{},
	}
}

type Update struct {
	UpdateID      int64    `json:"update_id"`
	Message       *Message `json:"message"`
	EditedMessage *Message `json:"edited_message"`
}

type Message struct {
	MessageID       int64  `json:"message_id"`
	MessageThreadID int64  `json:"message_thread_id"`
	Chat            Chat   `json:"chat"`
	From            *User  `json:"from"`
	SenderChat      *Chat  `json:"sender_chat"`
	Text            string `json:"text"`
	Caption         string `json:"caption"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// APIError — ошибка, вернувшаяся от Telegram.
type APIError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  int
}

func (e *APIError) Error() string {
	if e.Code == 0 {
		return fmt.Sprintf("%s: %s", e.Method, e.Description)
	}
	return fmt.Sprintf("%s: %d %s", e.Method, e.Code, e.Description)
}

// GetMe проверяет токен и возвращает данные бота.
func (c *Client) GetMe(ctx context.Context) (*User, error) {
	var u User
	if err := c.callJSON(ctx, "getMe", map[string]any{}, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// GetUpdates забирает обновления через long polling.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]Update, error) {
	payload := map[string]any{
		"offset":          offset,
		"timeout":         timeoutSec,
		"allowed_updates": []string{"message"},
	}
	var updates []Update
	if err := c.callJSON(ctx, "getUpdates", payload, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

func (c *Client) SendMessage(ctx context.Context, chatID, threadID int64, text string, replyTo int64) (*Message, error) {
	payload := map[string]any{
		"chat_id":    chatID,
		"text":       truncate(text, 4096),
		"parse_mode": "HTML",
	}
	if threadID != 0 {
		payload["message_thread_id"] = threadID
	}
	if replyTo != 0 {
		payload["reply_to_message_id"] = replyTo
		payload["allow_sending_without_reply"] = true
	}
	var msg Message
	if err := c.callJSON(ctx, "sendMessage", payload, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func (c *Client) EditMessageText(ctx context.Context, chatID, messageID int64, text string) error {
	payload := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       truncate(text, 4096),
		"parse_mode": "HTML",
	}
	return c.callJSON(ctx, "editMessageText", payload, nil)
}

func (c *Client) SendPhoto(ctx context.Context, chatID, threadID int64, path, caption string, replyTo int64) (*Message, error) {
	fields := map[string]string{
		"chat_id":    strconv.FormatInt(chatID, 10),
		"caption":    truncate(caption, 1024),
		"parse_mode": "HTML",
	}
	if threadID != 0 {
		fields["message_thread_id"] = strconv.FormatInt(threadID, 10)
	}
	if replyTo != 0 {
		fields["reply_to_message_id"] = strconv.FormatInt(replyTo, 10)
		fields["allow_sending_without_reply"] = "true"
	}
	var msg Message
	if err := c.upload(ctx, "sendPhoto", fields, "photo", path, false, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func (c *Client) EditMessageCaption(ctx context.Context, chatID, messageID int64, caption string) error {
	payload := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"caption":    truncate(caption, 1024),
		"parse_mode": "HTML",
	}
	return c.callJSON(ctx, "editMessageCaption", payload, nil)
}

func (c *Client) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	payload := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
	}
	return c.callJSON(ctx, "deleteMessage", payload, nil)
}

type EditMedia struct {
	ChatID    int64
	MessageID int64
	Path      string
	Caption   string
	Width     int
	Height    int
	Duration  int
}

// EditMessageMedia заменяет картинку статуса на готовое видео, сохраняя сообщение.
func (c *Client) EditMessageMedia(ctx context.Context, m EditMedia) (*Message, error) {
	media := map[string]any{
		"type":               "video",
		"media":              "attach://video",
		"caption":            truncate(m.Caption, 1024),
		"parse_mode":         "HTML",
		"supports_streaming": true,
	}
	if m.Width > 0 && m.Height > 0 {
		media["width"] = m.Width
		media["height"] = m.Height
	}
	if m.Duration > 0 {
		media["duration"] = m.Duration
	}
	raw, err := json.Marshal(media)
	if err != nil {
		return nil, fmt.Errorf("editMessageMedia: %w", err)
	}
	fields := map[string]string{
		"chat_id":    strconv.FormatInt(m.ChatID, 10),
		"message_id": strconv.FormatInt(m.MessageID, 10),
		"media":      string(raw),
	}
	var msg Message
	if err := c.upload(ctx, "editMessageMedia", fields, "video", m.Path, false, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

type Video struct {
	ChatID      int64
	ThreadID    int64
	ReplyTo     int64
	Path        string
	Caption     string
	Width       int
	Height      int
	Duration    int
	UseLocalRef bool // отправить file:// путь вместо загрузки (локальный Bot API)
}

func (c *Client) SendVideo(ctx context.Context, v Video) (*Message, error) {
	fields := map[string]string{
		"chat_id":            strconv.FormatInt(v.ChatID, 10),
		"caption":            truncate(v.Caption, 1024),
		"parse_mode":         "HTML",
		"supports_streaming": "true",
	}
	if v.ThreadID != 0 {
		fields["message_thread_id"] = strconv.FormatInt(v.ThreadID, 10)
	}
	if v.ReplyTo != 0 {
		fields["reply_to_message_id"] = strconv.FormatInt(v.ReplyTo, 10)
		fields["allow_sending_without_reply"] = "true"
	}
	if v.Width > 0 && v.Height > 0 {
		fields["width"] = strconv.Itoa(v.Width)
		fields["height"] = strconv.Itoa(v.Height)
	}
	if v.Duration > 0 {
		fields["duration"] = strconv.Itoa(v.Duration)
	}
	var msg Message
	if err := c.upload(ctx, "sendVideo", fields, "video", v.Path, v.UseLocalRef, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

type Document struct {
	ChatID      int64
	ThreadID    int64
	ReplyTo     int64
	Path        string
	Caption     string
	UseLocalRef bool
}

func (c *Client) SendDocument(ctx context.Context, d Document) (*Message, error) {
	fields := map[string]string{
		"chat_id":    strconv.FormatInt(d.ChatID, 10),
		"caption":    truncate(d.Caption, 1024),
		"parse_mode": "HTML",
	}
	if d.ThreadID != 0 {
		fields["message_thread_id"] = strconv.FormatInt(d.ThreadID, 10)
	}
	if d.ReplyTo != 0 {
		fields["reply_to_message_id"] = strconv.FormatInt(d.ReplyTo, 10)
		fields["allow_sending_without_reply"] = "true"
	}
	var msg Message
	if err := c.upload(ctx, "sendDocument", fields, "document", d.Path, d.UseLocalRef, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// callJSON выполняет обычный метод Bot API, повторяя запрос при 429 и 5xx.
func (c *Client) callJSON(ctx context.Context, method string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}

	const attempts = 3
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.methodURL(method), bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			err = c.hideToken(err)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if attempt < attempts {
				sleepCtx(ctx, time.Duration(attempt)*2*time.Second)
				continue
			}
			return fmt.Errorf("%s: %w", method, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("%s: чтение ответа: %w", method, readErr)
		}

		var ar apiResponse
		if err := json.Unmarshal(data, &ar); err != nil {
			return fmt.Errorf("%s: некорректный ответ (HTTP %d): %s", method, resp.StatusCode, truncate(string(data), 300))
		}
		if !ar.OK {
			apiErr := &APIError{Method: method, Code: ar.ErrorCode, Description: ar.Description}
			if ar.Parameters != nil {
				apiErr.RetryAfter = ar.Parameters.RetryAfter
			}
			if (apiErr.Code == 429 || apiErr.Code >= 500) && attempt < attempts {
				wait := time.Duration(apiErr.RetryAfter) * time.Second
				if wait <= 0 {
					wait = time.Duration(attempt) * 2 * time.Second
				}
				sleepCtx(ctx, wait)
				continue
			}
			return apiErr
		}
		if out != nil && len(ar.Result) > 0 {
			if err := json.Unmarshal(ar.Result, out); err != nil {
				return fmt.Errorf("%s: разбор результата: %w", method, err)
			}
		}
		return nil
	}
}

// callForm отправляет простой form-urlencoded запрос (нужен локальному Bot API
// для передачи file:// ссылок).
func (c *Client) callForm(ctx context.Context, method string, fields map[string]string, out any) error {
	form := url.Values{}
	for k, v := range fields {
		form.Set(k, v)
	}

	const attempts = 3
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.methodURL(method), strings.NewReader(form.Encode()))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := c.http.Do(req)
		if err != nil {
			err = c.hideToken(err)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if attempt < attempts {
				sleepCtx(ctx, time.Duration(attempt)*2*time.Second)
				continue
			}
			return fmt.Errorf("%s: %w", method, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("%s: чтение ответа: %w", method, readErr)
		}

		var ar apiResponse
		if err := json.Unmarshal(data, &ar); err != nil {
			return fmt.Errorf("%s: некорректный ответ (HTTP %d): %s", method, resp.StatusCode, truncate(string(data), 300))
		}
		if !ar.OK {
			apiErr := &APIError{Method: method, Code: ar.ErrorCode, Description: ar.Description}
			if ar.Parameters != nil {
				apiErr.RetryAfter = ar.Parameters.RetryAfter
			}
			if (apiErr.Code == 429 || apiErr.Code >= 500) && attempt < attempts {
				wait := time.Duration(apiErr.RetryAfter) * time.Second
				if wait <= 0 {
					wait = time.Duration(attempt) * 2 * time.Second
				}
				sleepCtx(ctx, wait)
				continue
			}
			return apiErr
		}
		if out != nil && len(ar.Result) > 0 {
			if err := json.Unmarshal(ar.Result, out); err != nil {
				return fmt.Errorf("%s: разбор результата: %w", method, err)
			}
		}
		return nil
	}
}

// upload отправляет multipart-запрос (видео или документ).
func (c *Client) upload(ctx context.Context, method string, fields map[string]string, fileField, filePath string, useLocalRef bool, out any) error {
	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("%s: файл недоступен: %w", method, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s: ожидался файл, а не каталог: %s", method, filePath)
	}

	if useLocalRef {
		fields[fileField] = fileURL(filePath)
		return c.callForm(ctx, method, fields, out)
	}

	const attempts = 3
	for attempt := 1; ; attempt++ {
		f, err := os.Open(filePath)
		if err != nil {
			return fmt.Errorf("%s: файл недоступен: %w", method, err)
		}

		pr, pw := io.Pipe()
		mw := multipart.NewWriter(pw)

		go func() {
			defer pw.Close()
			defer f.Close()
			for k, v := range fields {
				if err := mw.WriteField(k, v); err != nil {
					pw.CloseWithError(err)
					return
				}
			}
			part, err := mw.CreateFormFile(fileField, filepath.Base(filePath))
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			if _, err := io.Copy(part, f); err != nil {
				pw.CloseWithError(err)
				return
			}
			_ = mw.Close()
		}()

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.methodURL(method), pr)
		if err != nil {
			pr.CloseWithError(err)
			f.Close()
			return err
		}
		req.ContentLength = -1
		req.Header.Set("Content-Type", mw.FormDataContentType())

		resp, err := c.http.Do(req)
		if err != nil {
			err = c.hideToken(err)
			pr.CloseWithError(err)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if attempt < attempts {
				sleepCtx(ctx, time.Duration(attempt)*3*time.Second)
				continue
			}
			return fmt.Errorf("%s: %w", method, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		resp.Body.Close()
		// Освобождает писателя, если сервер ответил, не дочитав тело.
		pr.Close()
		if readErr != nil {
			return fmt.Errorf("%s: чтение ответа: %w", method, readErr)
		}

		var ar apiResponse
		if err := json.Unmarshal(data, &ar); err != nil {
			return fmt.Errorf("%s: некорректный ответ (HTTP %d): %s", method, resp.StatusCode, truncate(string(data), 300))
		}
		if !ar.OK {
			apiErr := &APIError{Method: method, Code: ar.ErrorCode, Description: ar.Description}
			if ar.Parameters != nil {
				apiErr.RetryAfter = ar.Parameters.RetryAfter
			}
			if (apiErr.Code == 429 || apiErr.Code >= 500) && attempt < attempts {
				wait := time.Duration(apiErr.RetryAfter) * time.Second
				if wait <= 0 {
					wait = time.Duration(attempt) * 3 * time.Second
				}
				sleepCtx(ctx, wait)
				continue
			}
			return apiErr
		}
		if out != nil && len(ar.Result) > 0 {
			if err := json.Unmarshal(ar.Result, out); err != nil {
				return fmt.Errorf("%s: разбор результата: %w", method, err)
			}
		}
		return nil
	}
}

func (c *Client) methodURL(method string) string {
	return fmt.Sprintf("%s/bot%s/%s", c.base, c.token, method)
}

// fileURL превращает путь в file://-ссылку, понятную локальному Bot API.
func fileURL(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	// Linux: /home/x → file:///home/x, Windows: C:/x → file://C:/x.
	return "file://" + filepath.ToSlash(abs)
}

// hideToken убирает токен из текста сетевых ошибок: url.Error печатает полный
// адрес запроса, а в нём есть bot<token>.
func (c *Client) hideToken(err error) error {
	if err == nil || c.token == "" {
		return nil
	}
	msg := err.Error()
	if !strings.Contains(msg, c.token) {
		return err
	}
	return errors.New(strings.ReplaceAll(msg, c.token, "***"))
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}

func isTelegramTooBig(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	desc := strings.ToLower(apiErr.Description)
	return strings.Contains(desc, "too big") || strings.Contains(desc, "too large") || apiErr.Code == 413
}
