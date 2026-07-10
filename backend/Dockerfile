FROM golang:1.23-bookworm AS build

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        g++ \
        libharfbuzz-dev \
        libwoff-dev \
        pkg-config \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/emfont-controller ./cmd/controller
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/emfont-migrate ./cmd/migrate

FROM debian:bookworm-slim AS runtime

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        libharfbuzz-subset0 \
        libharfbuzz0b \
        libwoff1 \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --uid 10001 emfont

WORKDIR /app
COPY --from=build /out/emfont-controller /usr/local/bin/emfont-controller
COPY --from=build /out/emfont-migrate /usr/local/bin/emfont-migrate
COPY db/migrations ./db/migrations

USER emfont
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/emfont-controller"]
