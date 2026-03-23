# --- Builder stage -----------------------------------------------------------
FROM --platform=linux/amd64 golang:1.25.1-bookworm AS build

ENV GO111MODULE=on
ARG GOPROXY="https://proxy.golang.org,direct"
ARG TOKENIZERS_LIB_VERSION="1.26.0"
RUN go env -w GOPROXY=${GOPROXY}

ENV SRC_VERSION=1.0.4

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN mkdir -p /opt/tokenizers && \
    curl -fsSL "https://github.com/daulet/tokenizers/releases/download/v${TOKENIZERS_LIB_VERSION}/libtokenizers.linux-amd64.tar.gz" -o /tmp/libtokenizers.tar.gz && \
    tar -xzf /tmp/libtokenizers.tar.gz -C /opt/tokenizers && \
    rm -f /tmp/libtokenizers.tar.gz
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 CGO_LDFLAGS="-L/opt/tokenizers" \
    go build -tags tokenizers_hf -trimpath -ldflags="-s -w" -o /out/pipelineai ./cmd/pipelineai

# --- Runtime stage -----------------------------------------------------------
FROM build AS pai

ENV DEBIAN_FRONTEND=noninteractive

# Установка необходимых инструментов для работы агента.
RUN apt-get update && \
        apt-get install -y --no-install-recommends \
        ca-certificates bash curl jq ripgrep python3 python3-venv python3-pip \
        redis-tools postgresql-client make git \
        clang libcurl4-openssl-dev && \
        update-ca-certificates && \
        apt-get clean && \
        rm -rf /var/lib/apt/lists/*

RUN go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.4.0

RUN go install golang.org/x/tools/cmd/goimports@latest

COPY --from=build /out/pipelineai /usr/local/bin/pipelineai

ENV CONFIG_VERSION=1.0.0

RUN mkdir -p /usr/local/share/pipelineai/configs /usr/local/share/pipelineai/prompts /usr/local/share/pipelineai/rules

COPY ci/configs /usr/local/share/pipelineai/configs
COPY ci/prompts /usr/local/share/pipelineai/prompts
COPY ci/rules /usr/local/share/pipelineai/rules

RUN chmod +x /usr/local/bin/pipelineai && pipelineai --help >/dev/null 2>&1 || true

# В базовом образе не запускаем агент автоматически; контейнер держим живым
CMD ["bash", "-lc", "sleep infinity"]
