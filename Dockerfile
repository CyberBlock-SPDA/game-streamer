FROM golang:1.26-trixie AS agent
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/agent cmd/agent
COPY internal internal
RUN CGO_ENABLED=0 go build -trimpath -o /game-agent ./cmd/agent

FROM debian:trixie-slim AS encoder
RUN apt-get update && apt-get install -y --no-install-recommends gcc libc6-dev libavcodec-dev libavutil-dev pkg-config && rm -rf /var/lib/apt/lists/*
COPY native/encode.c /src/encode.c
RUN cc -std=c11 -Wall -Wextra -Werror -O2 /src/encode.c -o /encoder $(pkg-config --libs --cflags libavcodec libavutil)

FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends chocolate-doom freedoom xvfb x11-utils xdotool ffmpeg && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 10001 --create-home --shell /usr/sbin/nologin player \
    && install -d -m 1777 /tmp/.X11-unix
ENV PATH="/usr/games:${PATH}" DISPLAY=:99 SDL_VIDEODRIVER=x11 SDL_AUDIODRIVER=dummy SDL_RENDER_DRIVER=software
COPY --from=agent /game-agent /usr/local/bin/game-agent
COPY --from=encoder /encoder /usr/local/bin/game-encoder
USER 10001:10001
WORKDIR /home/player
EXPOSE 7000/tcp 8000/udp
ENTRYPOINT ["/usr/local/bin/game-agent"]
