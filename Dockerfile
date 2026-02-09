# --- Builder stage -----------------------------------------------------------
FROM --platform=linux/amd64 golang:1.25.1-bookworm AS build

ENV GO111MODULE=on
ARG GOPROXY="https://proxy.golang.org,direct"
RUN go env -w GOPROXY=${GOPROXY}

ENV SRC_VERSION=1.0.4

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/pipelineai ./cmd/pipelineai

# --- Runtime stage -----------------------------------------------------------
FROM build AS pai

ENV DEBIAN_FRONTEND=noninteractive

ENV SWIFTLINT_VERSION=0.62.2
ENV KTLINT_VERSION=0.50.0

# Установка необходимых инструментов для работы агента: bash, curl, jq, ripgrep, python3, redis-cli, psql, node, npm, openjdk, golangci-lint, ktlint, swiftlint и др.
RUN apt-get update && \
        apt-get install -y --no-install-recommends \
        ca-certificates bash curl jq ripgrep python3 python3-venv python3-pip \
        redis-tools postgresql-client make git nodejs npm openjdk-17-jre-headless \
        clang libcurl4-openssl-dev unzip && \
        curl -L "https://github.com/realm/SwiftLint/releases/download/${SWIFTLINT_VERSION}/swiftlint_linux_amd64.zip" -o /tmp/swiftlint.zip && \
        unzip /tmp/swiftlint.zip -d /tmp/swiftlint && \
        mv /tmp/swiftlint/swiftlint /usr/local/bin/swiftlint && \
        chmod a+x /usr/local/bin/swiftlint && \
        rm -rf /tmp/swiftlint /tmp/swiftlint.zip && \
        curl -sSLO "https://github.com/pinterest/ktlint/releases/download/${KTLINT_VERSION}/ktlint" && \
        chmod a+x ktlint && \
        mv ktlint /usr/local/bin/ && \
        npm install -g eslint prettier && \
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
