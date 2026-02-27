#!/usr/bin/env bash
set -euo pipefail

# Обёртка для smoke-тестов: поднимает локальный mock-LLM (OpenAI Chat Completions),
# выставляет LLM_* переменные окружения и выполняет указанную команду.

: "${PAI_BIN:?PAI_BIN is required (path to pipelineai binary)}"

if [ ! -x "$PAI_BIN" ]; then
  echo "pipelineai binary is not executable: $PAI_BIN" >&2
  exit 1
fi

TMP_DIR="${PAI_MOCK_LLM_TMP_DIR:-.agent/mock-llm}"
URL_FILE="${TMP_DIR}/url.txt"
LOG_FILE="${TMP_DIR}/mock-llm.log"
ADDR="${PAI_MOCK_LLM_ADDR:-127.0.0.1:0}"

mkdir -p "$TMP_DIR"
rm -f "$URL_FILE" "$LOG_FILE"

"$PAI_BIN" mock-llm --addr "$ADDR" --write-url-file "$URL_FILE" >"$LOG_FILE" 2>&1 &
pid="$!"

cleanup() {
  kill "$pid" >/dev/null 2>&1 || true
  wait "$pid" >/dev/null 2>&1 || true
}
trap cleanup EXIT

for _ in $(seq 1 100); do
  if [ -s "$URL_FILE" ]; then
    break
  fi
  sleep 0.05
done

if [ ! -s "$URL_FILE" ]; then
  if [ -s "$LOG_FILE" ]; then
    cat "$LOG_FILE" >&2
  fi
  echo "mock llm did not become ready" >&2
  exit 1
fi

mock_url="$(tr -d '\n' < "$URL_FILE")"

export LLM_BASE_URL="${mock_url}"
export LLM_MODEL="${LLM_MODEL:-openai/gpt-oss-20b}"
export LLM_API_KEY="${LLM_API_KEY:-}"

exec "$@"
