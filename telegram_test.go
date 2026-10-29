package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSendMessage(t *testing.T) {
	var method string
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = filepath.Base(r.URL.Path)
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("не разобрал JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true,"result":{"message_id":7,"chat":{"id":1},"text":"hi"}}`)
	}))
	defer srv.Close()

	client := NewClient("TOKEN", srv.URL)
	msg, err := client.SendMessage(context.Background(), 1, "привет", 0)
	if err != nil {
		t.Fatal(err)
	}
	if method != "sendMessage" {
		t.Errorf("метод=%q", method)
	}
	if msg.MessageID != 7 {
		t.Errorf("message_id=%d", msg.MessageID)
	}
	if payload["text"] != "привет" {
		t.Errorf("text=%v", payload["text"])
	}
	if payload["chat_id"] != float64(1) {
		t.Errorf("chat_id=%v", payload["chat_id"])
	}
}

func TestUploadVideo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clip.mp4")
	content := strings.Repeat("video-bytes", 1000)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	var gotChatID, gotCaption, gotFile string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			t.Errorf("ожидался multipart, получен %q", r.Header.Get("Content-Type"))
		}
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Errorf("multipart: %v", err)
			return
		}
		gotChatID = r.FormValue("chat_id")
		gotCaption = r.FormValue("caption")
		f, _, err := r.FormFile("video")
		if err != nil {
			t.Errorf("нет файла: %v", err)
			return
		}
		defer f.Close()
		data, _ := io.ReadAll(f)
		gotFile = string(data)

		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true,"result":{"message_id":9}}`)
	}))
	defer srv.Close()

	client := NewClient("TOKEN", srv.URL)
	msg, err := client.SendVideo(context.Background(), Video{ChatID: 5, Path: path, Caption: "тест", Width: 640, Height: 360, Duration: 3})
	if err != nil {
		t.Fatal(err)
	}
	if msg.MessageID != 9 {
		t.Errorf("message_id=%d", msg.MessageID)
	}
	if gotChatID != "5" || gotCaption != "тест" {
		t.Errorf("chat_id=%q caption=%q", gotChatID, gotCaption)
	}
	if gotFile != content {
		t.Errorf("файл повреждён: %d байт вместо %d", len(gotFile), len(content))
	}
}

func TestUploadLocalRef(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	var fileRef string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			t.Errorf("ожидался form-urlencoded, получен %q", r.Header.Get("Content-Type"))
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("form: %v", err)
		}
		fileRef = r.PostForm.Get("video")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	defer srv.Close()

	client := NewClient("TOKEN", srv.URL)
	if _, err := client.SendVideo(context.Background(), Video{ChatID: 5, Path: path, UseLocalRef: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fileRef, "file://") || !strings.HasSuffix(fileRef, "clip.mp4") {
		t.Errorf("ожидалась file:// ссылка, получено %q", fileRef)
	}
}

func TestAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: file is too big"}`)
	}))
	defer srv.Close()

	client := NewClient("TOKEN", srv.URL)
	_, err := client.SendDocument(context.Background(), Document{ChatID: 1, Path: filepath.Join(t.TempDir(), "missing.mp4")})
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}

	path := filepath.Join(t.TempDir(), "doc.pdf")
	os.WriteFile(path, []byte("data"), 0o644)
	_, err = client.SendDocument(context.Background(), Document{ChatID: 1, Path: path})
	if err == nil {
		t.Fatal("ожидалась ошибка API")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("ожидался *APIError, получено %T: %v", err, err)
	}
	if !isTelegramTooBig(err) {
		t.Errorf("isTelegramTooBig не распознал ошибку: %v", err)
	}
}

func TestGetMe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true,"result":{"id":42,"is_bot":true,"first_name":"Video","username":"video_bot"}}`)
	}))
	defer srv.Close()

	me, err := NewClient("TOKEN", srv.URL).GetMe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if me.ID != 42 || me.Username != "video_bot" {
		t.Errorf("некорректные данные: %+v", me)
	}
}
