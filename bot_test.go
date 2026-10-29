package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtractURLs(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "обычные ссылки",
			text: "смотри https://youtu.be/dQw4w9WgXcQ и https://x.com/user/status/123?s=20.",
			want: []string{"https://youtu.be/dQw4w9WgXcQ", "https://x.com/user/status/123?s=20"},
		},
		{
			name: "скобки и знаки",
			text: "(https://vimeo.com/12345), https://youtu.be/abc!",
			want: []string{"https://vimeo.com/12345", "https://youtu.be/abc"},
		},
		{
			name: "дубликаты",
			text: "https://youtu.be/abc https://youtu.be/abc",
			want: []string{"https://youtu.be/abc"},
		},
		{
			name: "без схемы",
			text: "youtube.com/watch?v=abc и youtu.be/xyz",
			want: []string{"https://youtube.com/watch?v=abc", "https://youtu.be/xyz"},
		},
		{
			name: "нет ссылок",
			text: "привет, как дела?",
			want: nil,
		},
		{
			name: "не видео-домен без схемы",
			text: "example.com/page",
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractURLs(tc.text)
			if len(got) != len(tc.want) {
				t.Fatalf("получено %v, ожидалось %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("ссылка %d: получено %q, ожидалось %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestParseProgress(t *testing.T) {
	tests := []struct {
		line  string
		ok    bool
		stage string
		pct   float64
		speed string
		eta   string
	}{
		{line: "[download]  42.3% of ~ 10.00MiB at 1.50MiB/s ETA 00:05", ok: true, stage: "download", pct: 42.3, speed: "1.50MiB/s", eta: "00:05"},
		{line: "[download]   0.0% of 3.43MiB at Unknown B/s ETA Unknown", ok: true, stage: "download"},
		{line: "[Merger] Merging formats into \"video.mp4\"", ok: true, stage: "merge"},
		{line: "[download] Destination: video.mp4", ok: true, stage: "download"},
		{line: "[youtube] Extracting URL: https://youtu.be/abc", ok: false},
	}

	for _, tc := range tests {
		p, ok := parseProgress(tc.line)
		if ok != tc.ok {
			t.Fatalf("%q: ok=%v, ожидалось %v", tc.line, ok, tc.ok)
		}
		if !ok {
			continue
		}
		if p.Stage != tc.stage {
			t.Errorf("%q: stage=%q, ожидалось %q", tc.line, p.Stage, tc.stage)
		}
		if p.Percent != tc.pct {
			t.Errorf("%q: percent=%v, ожидалось %v", tc.line, p.Percent, tc.pct)
		}
		if tc.speed != "" && p.Speed != tc.speed {
			t.Errorf("%q: speed=%q, ожидалось %q", tc.line, p.Speed, tc.speed)
		}
		if tc.eta != "" && p.ETA != tc.eta {
			t.Errorf("%q: eta=%q, ожидалось %q", tc.line, p.ETA, tc.eta)
		}
	}
}

func TestProgressText(t *testing.T) {
	got := progressText(Progress{Percent: 42.6, Speed: "1.5MiB/s", ETA: "00:05", Quality: "720p"})
	for _, want := range []string{"43%", "720p", "1.5MiB/s", "00:05"} {
		if !strings.Contains(got, want) {
			t.Errorf("в тексте %q нет %q", got, want)
		}
	}

	if got := progressText(Progress{Percent: 0}); !strings.Contains(got, "Скачиваю") {
		t.Errorf("неожиданный текст: %q", got)
	}
	if got := progressText(Progress{Stage: "merge"}); !strings.Contains(got, "Склеиваю") {
		t.Errorf("неожиданный текст: %q", got)
	}
}

func TestMergeProgress(t *testing.T) {
	old := Progress{Percent: 50, Speed: "2MiB/s", ETA: "00:10", Quality: "1080p", Stage: "download"}
	merged := mergeProgress(old, Progress{Percent: 60, Stage: "download"})
	if merged.Percent != 60 || merged.Speed != "2MiB/s" || merged.ETA != "00:10" || merged.Quality != "1080p" {
		t.Errorf("некорректное слияние: %+v", merged)
	}

	merged = mergeProgress(old, Progress{Stage: "merge"})
	if merged.Stage != "merge" || merged.Percent != 0 {
		t.Errorf("стадия merge должна сбрасывать процент: %+v", merged)
	}

	merged = mergeProgress(old, Progress{Percent: 0, Stage: "download", Quality: "720p"})
	if merged.Percent != 0 || merged.Quality != "720p" {
		t.Errorf("смена качества должна сбрасывать старый процент: %+v", merged)
	}
}

func TestHelpers(t *testing.T) {
	if got := formatDuration(65); got != "1:05" {
		t.Errorf("formatDuration(65)=%q", got)
	}
	if got := formatDuration(3665); got != "1:01:05" {
		t.Errorf("formatDuration(3665)=%q", got)
	}
	if got := humanSize(1024); got != "1.0 KiB" {
		t.Errorf("humanSize(1024)=%q", got)
	}
	if got := truncate("привет", 3); got != "пр…" {
		t.Errorf("truncate=%q", got)
	}
	if got := cleanError("ERROR: a\n\nb\tc"); strings.ContainsAny(got, "\n\t") {
		t.Errorf("cleanError оставил переводы строк: %q", got)
	}
}

func TestUserLocks(t *testing.T) {
	locks := newUserLocks()
	if !locks.tryLock(1) {
		t.Fatal("первая блокировка должна проходить")
	}
	if locks.tryLock(1) {
		t.Fatal("повторная блокировка того же пользователя должна отклоняться")
	}
	if !locks.tryLock(2) {
		t.Fatal("другой пользователь не должен блокироваться")
	}
	locks.unlock(1)
	if !locks.tryLock(1) {
		t.Fatal("после unlock блокировка должна проходить")
	}
}

func TestAllowed(t *testing.T) {
	open := &Bot{cfg: &Config{}}
	if !open.allowed(42) {
		t.Error("без ALLOWED_USERS доступ должен быть открыт")
	}
	private := &Bot{cfg: &Config{AllowedUsers: map[int64]bool{7: true}}}
	if private.allowed(42) {
		t.Error("посторонний пользователь не должен иметь доступ")
	}
	if !private.allowed(7) {
		t.Error("разрешённый пользователь должен иметь доступ")
	}
}

func TestUnauthorizedIsSilent(t *testing.T) {
	// API намеренно nil: если бот попытается ответить, тест упадёт с паникой.
	b := &Bot{cfg: &Config{AllowedUsers: map[int64]bool{7: true}}}
	for _, text := range []string{
		"https://youtu.be/dQw4w9WgXcQ",
		"привет",
		"/start",
		"/help",
		"/v https://youtu.be/dQw4w9WgXcQ",
		"/unknown",
	} {
		b.handle(&Message{
			MessageID: 1,
			Chat:      Chat{ID: 777, Type: "private"},
			From:      &User{ID: 42, FirstName: "Test"},
			Text:      text,
		})
	}
}

func TestGroupIgnoresPlainLinks(t *testing.T) {
	// jobs и userLocks намеренно nil: попытка взять ссылку в работу упадёт с паникой.
	b := &Bot{cfg: &Config{}}
	for _, chatType := range []string{"group", "supergroup"} {
		for _, text := range []string{
			"https://youtu.be/dQw4w9WgXcQ",
			"смотрите https://x.com/user/status/123",
			"привет",
		} {
			b.handle(&Message{
				MessageID: 1,
				Chat:      Chat{ID: -100, Type: chatType},
				From:      &User{ID: 42},
				Text:      text,
			})
		}
	}
}

func TestAnonymousAdminEnqueues(t *testing.T) {
	b := &Bot{cfg: &Config{}, jobs: make(chan job, 2), userLocks: newUserLocks()}
	b.handle(anonymousMessage("/v https://youtu.be/dQw4w9WgXcQ"))

	select {
	case j := <-b.jobs:
		if j.threadID != 77 {
			t.Errorf("threadID=%d, ожидался 77", j.threadID)
		}
		if j.userID != -100200 {
			t.Errorf("userID=%d, ожидался ID группы", j.userID)
		}
		if len(j.urls) != 1 || j.urls[0] != "https://youtu.be/dQw4w9WgXcQ" {
			t.Errorf("urls=%v", j.urls)
		}
	default:
		t.Fatal("анонимный админ должен ставить задачу в очередь")
	}
}

func TestAnonymousAdminAllowlist(t *testing.T) {
	msg := anonymousMessage("/v https://youtu.be/dQw4w9WgXcQ")

	denied := &Bot{
		cfg:       &Config{AllowedUsers: map[int64]bool{7: true}},
		jobs:      make(chan job, 1),
		userLocks: newUserLocks(),
	}
	denied.handle(msg)
	if len(denied.jobs) != 0 {
		t.Error("анонимный админ не из списка не должен ставить задачу")
	}

	allowed := &Bot{
		cfg:       &Config{AllowedUsers: map[int64]bool{-100200: true}},
		jobs:      make(chan job, 1),
		userLocks: newUserLocks(),
	}
	allowed.handle(msg)
	if len(allowed.jobs) != 1 {
		t.Error("ID группы в ALLOWED_USERS должен разрешать анонимного админа")
	}
}

func TestBotMessageIgnored(t *testing.T) {
	b := &Bot{cfg: &Config{}, jobs: make(chan job, 1), userLocks: newUserLocks()}
	b.handle(&Message{
		Chat: Chat{ID: -100200, Type: "supergroup"},
		From: &User{ID: 123, IsBot: true},
		Text: "/v https://youtu.be/dQw4w9WgXcQ",
	})
	if len(b.jobs) != 0 {
		t.Error("сообщения от обычных ботов нужно игнорировать")
	}
}

func anonymousMessage(text string) *Message {
	return &Message{
		MessageID:       5,
		MessageThreadID: 77,
		Chat:            Chat{ID: -100200, Type: "supergroup"},
		From:            &User{ID: 1087968824, IsBot: true, FirstName: "GroupAnonymousBot"},
		SenderChat:      &Chat{ID: -100200, Type: "supergroup"},
		Text:            text,
	}
}

func TestIDAlwaysAvailable(t *testing.T) {
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("не разобрал JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	defer srv.Close()

	b := &Bot{
		cfg: &Config{AllowedUsers: map[int64]bool{7: true}},
		tg:  NewClient("TOKEN", srv.URL),
	}
	b.handle(&Message{
		MessageID:       1,
		MessageThreadID: 55,
		Chat:            Chat{ID: -100200, Type: "supergroup"},
		From:            &User{ID: 42},
		Text:            "/id@test_bot",
	})

	if payload == nil {
		t.Fatal("/id должен отвечать даже при закрытом доступе")
	}
	if payload["message_thread_id"] != float64(55) {
		t.Errorf("message_thread_id=%v", payload["message_thread_id"])
	}
	text, _ := payload["text"].(string)
	if !strings.Contains(text, "<code>42</code>") {
		t.Errorf("в ответе нет ID отправителя: %q", text)
	}
}

func TestCommandArg(t *testing.T) {
	tests := map[string]string{
		"/v https://youtu.be/dQw4w9WgXcQ":           "https://youtu.be/dQw4w9WgXcQ",
		"/v@test_bot https://x.com/user/status/123": "https://x.com/user/status/123",
		"/v https://a.com/1 https://b.com/2":        "https://a.com/1 https://b.com/2",
		"/v":                                        "",
		"/v   ":                                     "",
		"/start":                                    "",
	}
	for in, want := range tests {
		if got := commandArg(in); got != want {
			t.Errorf("commandArg(%q)=%q, ожидалось %q", in, got, want)
		}
	}
	if !isGroupChat("group") || !isGroupChat("supergroup") || isGroupChat("private") {
		t.Error("isGroupChat определяет тип чата неверно")
	}

	commands := map[string]string{
		"/v@test_bot https://youtu.be/x": "/v",
		"/ID":                            "/id",
		"/id@test_bot":                   "/id",
		"просто текст":                   "",
		"":                               "",
	}
	for in, want := range commands {
		if got := commandName(in); got != want {
			t.Errorf("commandName(%q)=%q, ожидалось %q", in, got, want)
		}
	}
}
