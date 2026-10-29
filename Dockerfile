# syntax=docker/dockerfile:1

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bot .

FROM python:3.13-slim

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PIP_NO_CACHE_DIR=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8

# ffmpeg — склейка видео/звука, Deno — JS-движок для YouTube (иначе часть
# форматов недоступна), yt-dlp[default] — основные экстракторы и зависимости.
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ffmpeg ca-certificates curl unzip tzdata libstdc++6; \
    curl -fsSL -o /tmp/deno-install.sh https://deno.land/install.sh; \
    DENO_INSTALL=/usr/local sh /tmp/deno-install.sh; \
    rm -f /tmp/deno-install.sh; \
    pip install -U "yt-dlp[default]"; \
    apt-get purge -y --auto-remove curl unzip; \
    rm -rf /var/lib/apt/lists/* /root/.cache; \
    deno --version; \
    yt-dlp --version

COPY --from=build /out/bot /usr/local/bin/bot

RUN useradd --create-home --uid 10001 --shell /usr/sbin/nologin bot \
    && mkdir -p /data \
    && chown bot:bot /data

WORKDIR /data
ENV WORK_DIR=/data
VOLUME ["/data"]

USER bot
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/bot"]
