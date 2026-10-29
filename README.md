# Telegram-бот «ссылка → видео»

Присылаете боту ссылку — он скачивает видео через [yt-dlp](https://github.com/yt-dlp/yt-dlp) и отправляет файлом с плеером. Работает с YouTube, X (Twitter), TikTok, Instagram, Vimeo, VK, Rutube, Reddit, Twitch и тысячами других сайтов, которые поддерживает yt-dlp.

Написан на Go без внешних зависимостей — только стандартная библиотека.

## Что умеет

- принимает до 3 ссылок в одном сообщении и обрабатывает их по очереди;
- показывает прогресс: процент, скорость, ETA и текущее качество;
- сам понижает качество, если файл не влезает в лимит Telegram;
- отправляет видео как `video` (с превью и стримингом), а при проблемах — документом;
- очередь задач и лимит одновременных загрузок, чтобы сервер не захлебнулся;
- ограничение доступа по Telegram ID, поддержка cookies и прокси для yt-dlp;
- в группах не реагирует на обычные ссылки — только на `/v <ссылка>`;
- работает в чатах с темами (forum) и с анонимными администраторами;
- чистит временные файлы, в том числе оставшиеся после аварийного завершения;
- поддерживает локальный Bot API для файлов до 2 ГБ;
- готовый Docker-образ: yt-dlp, ffmpeg и Deno уже внутри, снаружи нужен только `.env`.

## Требования

- Go 1.22 или новее;
- [yt-dlp](https://github.com/yt-dlp/yt-dlp#installation) в `PATH` (или путь в `YTDLP_PATH`);
- **ffmpeg** в `PATH` (или путь в `FFMPEG_PATH`) — без него YouTube-видео с раздельными дорожками не склеится;
- токен бота от [@BotFather](https://t.me/BotFather).

Для запуска на VPS в Docker нужен только Docker с Compose — Go, yt-dlp и ffmpeg ставить не нужно, см. [Docker и запуск на VPS](#docker-и-запуск-на-vps).

Установка на Debian/Ubuntu:

```bash
sudo apt update && sudo apt install -y ffmpeg python3-pip
sudo pip3 install -U yt-dlp
```

Windows (PowerShell):

```powershell
winget install Gyan.FFmpeg
pip install -U yt-dlp
```

## Быстрый старт

```bash
# 1. Настройки
cp .env.example .env
# откройте .env и вставьте BOT_TOKEN

# 2. Сборка и запуск
go build -o bot .
./bot
```

На Windows бинарник называется `bot.exe`, запуск: `.\bot.exe`.

Бот сам читает `.env` из текущего каталога. Переменные окружения имеют приоритет над файлом.

Если yt-dlp/ffmpeg лежат не в `PATH`, укажите полные пути:

```dotenv
YTDLP_PATH=C:\tools\yt-dlp.exe
FFMPEG_PATH=C:\tools\ffmpeg.exe
```

При первом запуске бот проверяет токен (`getMe`), версию yt-dlp и предупреждает, если ffmpeg не найден.

## Группы

В личном чате достаточно прислать ссылку. В группе бот намеренно не реагирует на обычные сообщения — ссылка принимается только командой:

```text
/v https://youtu.be/dQw4w9WgXcQ
```

Можно передать до 3 ссылок через пробел. Если в группе несколько ботов, используйте `/v@имя_бота ...`. Доступ проверяется по ID отправителя так же, как в личном чате.

В чатах с темами бот отвечает в ту же тему, где была команда.

**Анонимный администратор.** Telegram присылает такие сообщения от служебного `GroupAnonymousBot`, а реальный отправитель — группа. Бот это распознаёт и берёт ID группы в качестве отправителя. Если задан `ALLOWED_USERS`, добавьте туда ID группы (отрицательное число, например `-1001234567890`) — иначе анонимный админ останется без ответа, как любой посторонний. ID группы показывает команда `/id`, отправленная от её имени: она работает всегда, независимо от `ALLOWED_USERS`.

**Privacy mode** отключать не нужно: `/v` — обычная команда, и Telegram доставляет её боту даже при включённой приватности. Отключать режим имеет смысл, только если бот должен читать все сообщения в группе, а ему это не требуется.

## Настройки

| Переменная | По умолчанию | Назначение |
| --- | --- | --- |
| `BOT_TOKEN` | — | Токен от @BotFather, обязателен |
| `ALLOWED_USERS` | пусто | ID пользователей через запятую. Пусто — бот открыт всем; остальным бот не отвечает |
| `YTDLP_PATH` | `yt-dlp` | Путь к yt-dlp |
| `FFMPEG_PATH` | из `PATH` | Путь к ffmpeg |
| `WORK_DIR` | `<tmp>/tg-video-bot` | Каталог временных файлов. В Docker всегда `/data` |
| `TZ` | `UTC` | Часовой пояс для логов (`Europe/Moscow` и т. п.) |
| `MAX_UPLOAD_MB` | `50` | Лимит размера отправляемого файла, МБ |
| `MAX_HEIGHT` | `1080` | Максимальное качество по высоте; `0` — лучшее доступное |
| `CONCURRENCY` | `2` | Сколько видео качать одновременно |
| `JOB_TIMEOUT_MIN` | `20` | Таймаут одной загрузки, минут |
| `COOKIES_FILE` | — | `cookies.txt` для видео, требующих входа |
| `YTDLP_PROXY` | — | Прокси для yt-dlp, например `socks5://127.0.0.1:1080` |
| `TELEGRAM_API_BASE` | `https://api.telegram.org` | Адрес Bot API (для локального сервера) |
| `LOCAL_BOT_API` | `0` | `1` — локальный Bot API, лимит до 2 ГБ |

Свой ID можно узнать командой `/id` — она отвечает всегда, даже когда доступ закрыт `ALLOWED_USERS`. Остальные команды и ссылки посторонним недоступны; ID также показывает @userinfobot.

## Большие файлы (больше 50 МБ)

Обычный Bot API принимает файлы до 50 МБ. Чтобы отправлять до 2 ГБ, поднимите [локальный Bot API](https://github.com/tdlib/telegram-bot-api):

```bash
git clone --recursive https://github.com/tdlib/telegram-bot-api.git
cd telegram-bot-api
cmake -B build -DCMAKE_BUILD_TYPE=Release
cmake --build build --target telegram-bot-api
./build/telegram-bot-api --api-id=ВАШ_API_ID --api-hash=ВАШ_API_HASH --local --http-port=8081
```

`api_id` и `api_hash` берутся на https://my.telegram.org. Затем в `.env`:

```dotenv
TELEGRAM_API_BASE=http://127.0.0.1:8081
LOCAL_BOT_API=1
MAX_UPLOAD_MB=2000
```

Для файлов больше 50 МБ бот передаст серверу `file://`-ссылку, поэтому локальный Bot API и бот должны работать на одной машине (или видеть один каталог).

## YouTube и закрытые видео

YouTube часто требует cookies, особенно с серверных IP. Экспортируйте cookies браузера в формате Netscape (`cookies.txt`) любым расширением и укажите:

```dotenv
COOKIES_FILE=./cookies.txt
```

Instagram, Vimeo и часть постов X тоже нередко отдают видео только с cookies. Файл с cookies — секрет: он даёт доступ к аккаунту, не публикуйте его.

## Docker и запуск на VPS

В образе уже есть всё необходимое:

- Go-бинарник бота;
- `yt-dlp[default]` — с зависимостями для X, Instagram и обхода части защит;
- `ffmpeg` — склейка раздельных дорожек видео и звука;
- `Deno` — JS-движок, без которого YouTube отдаёт не все форматы;
- `tzdata` и корневые сертификаты.

Бот работает через long polling, поэтому входящие порты открывать не нужно.

### Первый запуск

На Ubuntu/Debian установите Docker (если ещё не установлен):

```bash
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker "$USER"
# перелогиньтесь, чтобы применилась группа docker
```

Скопируйте проект на сервер:

```bash
rsync -av --exclude data --exclude .env ./ user@server:/opt/tg-video-bot/
ssh user@server
cd /opt/tg-video-bot
```

Создайте конфиг и запустите:

```bash
cp .env.example .env
nano .env                      # BOT_TOKEN, ALLOWED_USERS
docker compose up -d --build
docker compose logs -f --tail=100
```

Первая сборка занимает 2–5 минут: скачиваются ffmpeg, Deno и yt-dlp.

Проверка:

```bash
docker compose ps
docker stats --no-stream tg-video-bot
```

В логах должно появиться `подключён как @ваш_бот` и `бот запущен`.

### Без compose

```bash
docker build -t tg-video-bot .
docker run -d --name tg-video-bot --restart unless-stopped --init \
  -v tg-video-data:/data --env-file .env tg-video-bot
docker logs -f tg-video-bot
```

### Обновление

yt-dlp обновляется почти каждую неделю — пересобирайте образ:

```bash
cd /opt/tg-video-bot
docker compose build --pull --no-cache
docker compose up -d
```

`--no-cache` нужен, чтобы слой с `pip install yt-dlp` не взялся из кеша.

### Данные, права и логи

- `/data` подключён как именованный том `bot-data`; после отправки видео файлы удаляются.
- `WORK_DIR` в `.env` можно не менять: compose принудительно задаёт `/data`.
- Бот работает от пользователя `bot` (uid 10001). При переходе на bind-mount (`./data:/data`) выполните `sudo chown -R 10001:10001 ./data`.
- Логи ограничены 10 МБ × 3 файла. Смотреть: `docker compose logs -f`.
- Полная очистка временных файлов: `docker compose down -v` (удалит том).

### cookies.txt

Положите `cookies.txt` рядом с `docker-compose.yml`, раскомментируйте в сервисе:

```yaml
    volumes:
      - bot-data:/data
      - ./cookies.txt:/data/cookies.txt:ro
```

и укажите в `.env`:

```dotenv
COOKIES_FILE=/data/cookies.txt
```

Том можно оставить только для чтения: бот копирует файл в рабочий каталог каждой загрузки, поэтому yt-dlp не изменяет оригинал, а параллельные задачи не конфликтуют. Новые cookies подхватываются со следующей загрузки. Если файл заменяется на хосте целиком (`rsync`, атомарное сохранение редактора), одиночный bind-mount может продолжать отдавать старую версию — в этом случае перезапустите контейнер или смонтируйте каталог.

Файл должен быть читаем пользователю `bot` (uid 10001): `sudo chown 10001:10001 cookies.txt && chmod 600 cookies.txt`.

### Большие файлы (>50 МБ)

Стандартный Bot API принимает до 50 МБ. Для 2 ГБ нужен локальный Bot API, который видит те же файлы по пути `/data`. Проще всего запустить его отдельным контейнером с тем же томом `bot-data:/data` и указать:

```dotenv
TELEGRAM_API_BASE=http://telegram-bot-api:8081
LOCAL_BOT_API=1
MAX_UPLOAD_MB=2000
```

Если Bot API работает на хосте, а не в Docker, путь `file:///data/...` ему не виден — тогда проще запускать бота без Docker (см. раздел о локальном Bot API выше).

## systemd

```ini
[Unit]
Description=Telegram video bot
After=network-online.target
Wants=network-online.target

[Service]
WorkingDirectory=/opt/tgvideo
EnvironmentFile=/opt/tgvideo/.env
ExecStart=/opt/tgvideo/bot
Restart=always
RestartSec=5
User=tgvideo

[Install]
WantedBy=multi-user.target
```

## Как это работает

```
main.go        запуск, проверка yt-dlp, graceful shutdown
config.go      переменные окружения
env.go         чтение .env
telegram.go    Bot API: long polling, сообщения, multipart-загрузка
downloader.go  вызов yt-dlp, парсинг прогресса, подбор качества
bot.go         команды, очередь, прогресс, отправка файла
```

1. Бот получает сообщение через `getUpdates`, вытаскивает ссылки регулярными выражениями.
2. Задача попадает в очередь; воркер запускает `yt-dlp` с ограничением качества и размера.
3. yt-dlp скачивает видео и метаданные; бот читает `*.info.json` (заголовок, длительность, размеры).
4. Если файл больше лимита, попытка повторяется с меньшим разрешением (лестница 1080p → 720p → 480p → 360p → worst).
5. Готовый файл отправляется через `sendVideo`, при ошибке — через `sendDocument`, после чего временный каталог удаляется.

Ссылка передаётся yt-dlp после `--`, поэтому её нельзя использовать как аргумент командной строки. Поддерживаются только `http(s)://`.

## Тесты

```bash
go test ./...
```

Проверка на реальном видео (нужны yt-dlp и ffmpeg):

```bash
TEST_URL='https://www.youtube.com/watch?v=jNQXAC9IVRw' go test -run TestDownloaderLive -v
```

## Ограничения

- плейлисты не скачиваются: `--no-playlist`, обрабатывается только само видео;
- live-трансляции и видео, требующие DRM/входа, могут не скачаться;
- для видео с раздельными дорожками нужен ffmpeg;
- бот не предназначен для публичного запуска: любой пользователь сможет качать видео через ваш сервер. Ограничьте доступ через `ALLOWED_USERS` и следите за диском;
- скачивайте только то, что разрешено правилами сервиса и авторскими правами.
