.DEFAULT_GOAL := help

ENV_FILE := .env
TPL_ENV := .tpl.env
BIN_DIR := bin
# По умолчанию используем локально собранный бинарь из ./bin.
# При необходимости можно переопределить: `make BINARY=pipelineai ...`
BINARY ?= $(CURDIR)/$(BIN_DIR)/pipelineai

# По умолчанию smoke/демо-цели используют локальный мок LLM, чтобы не требовать внешний LLM и ключи.
# Отключить можно так: `make PAI_USE_MOCK_LLM=0 run-smoke`
PAI_USE_MOCK_LLM ?= 1
MOCK_LLM_WRAPPER :=
ifeq ($(PAI_USE_MOCK_LLM),1)
MOCK_LLM_WRAPPER := PAI_BIN="$(BINARY)" ./scripts/with-mock-llm.sh
endif
SYSTEM ?= Ты - ассистент PipelineAI. Отвечай кратко.
MESSAGE ?= Проверка связи.
CONFIG ?= docs/examples/configs/minimal-llm.yaml
STEP ?= produce_manifest
ARTIFACT_DIR ?= .agent/artifacts
RUN_FLAGS ?=
TOKENIZERS_LIB_VERSION ?= 1.26.0
TOKENIZERS_LIB_DIR_CMD = TOKENIZERS_LIB_VERSION="$(TOKENIZERS_LIB_VERSION)" ./scripts/ensure-tokenizers-lib.sh
BUILD_GOFLAGS = GO111MODULE=on CGO_ENABLED=1 CGO_LDFLAGS="-L$$($(TOKENIZERS_LIB_DIR_CMD))"
BUILD_GOTAGS = -tags tokenizers_hf

.PHONY: help
help: ## Выводит доступные цели make
	@grep -E '^[a-zA-Z0-9_-]+:.*##' Makefile | sort | while IFS= read -r line; do \
		target=$$(echo $$line | cut -d':' -f1); \
		desc=$$(echo $$line | cut -d'#' -f3-); \
		printf '\033[1;32m%s\033[0m: %s\n' "$$target" "$$desc"; \
	done

.PHONY: init
init: ## Создаёт .env из .tpl.env, если файла ещё нет
	@if [ -f $(ENV_FILE) ]; then \
		echo ".env уже существует"; \
	else \
		cp $(TPL_ENV) $(ENV_FILE); \
		echo "Создан $(ENV_FILE) на основе $(TPL_ENV)"; \
	fi

.PHONY: build
build: ## Собирает бинарь pipelineai в каталоге ./bin
	@mkdir -p $(BIN_DIR)
	$(BUILD_GOFLAGS) go build $(BUILD_GOTAGS) -o $(BINARY) ./cmd/pipelineai

.PHONY: run-smoke
run-smoke: build ## Запускает smoke-команду llm-smoke (MESSAGE и SYSTEM можно переопределить)
	@[ -f $(ENV_FILE) ] || $(MAKE) init
	$(MOCK_LLM_WRAPPER) $(BINARY) llm-smoke --system "$(SYSTEM)" --user "$(MESSAGE)"

.PHONY: run-step
run-step: build ## Запускает один шаг type=llm из конфигурации (CONFIG, STEP, ARTIFACT_DIR можно переопределить)
	@[ -f $(ENV_FILE) ] || $(MAKE) init
	$(MOCK_LLM_WRAPPER) $(BINARY) run --config "$(CONFIG)" --execute-step "$(STEP)" --artifact-dir "$(ARTIFACT_DIR)" $(RUN_FLAGS)

.PHONY: run-agent-loop
run-agent-loop: build ## Запускает примерную конфигурацию agent-loop smoke (docs/examples/configs/agent-loop-smoke.yaml)
	@[ -f $(ENV_FILE) ] || $(MAKE) init
	$(MOCK_LLM_WRAPPER) $(BINARY) run --config docs/examples/configs/agent-loop-smoke.yaml --execute-step agent_loop_smoke --artifact-dir "$(ARTIFACT_DIR)" $(RUN_FLAGS)

.PHONY: run-smoke-tools
run-smoke-tools: build ## Запускает smoke конфиг с вызовом shell и apply_patch (docs/examples/configs/tools-smoke.yaml)
	@[ -f $(ENV_FILE) ] || $(MAKE) init
	$(BINARY) run --config docs/examples/configs/tools-smoke.yaml --execute-step tools_smoke_init --artifact-dir "$(ARTIFACT_DIR)" $(RUN_FLAGS)
	$(MOCK_LLM_WRAPPER) $(BINARY) run --config docs/examples/configs/tools-smoke.yaml --execute-step tools_smoke_shell --artifact-dir "$(ARTIFACT_DIR)" $(RUN_FLAGS)
	$(MOCK_LLM_WRAPPER) $(BINARY) run --config docs/examples/configs/tools-smoke.yaml --execute-step tools_smoke_apply --artifact-dir "$(ARTIFACT_DIR)" $(RUN_FLAGS)
	$(BINARY) run --config docs/examples/configs/tools-smoke.yaml --execute-step tools_smoke_verify --artifact-dir "$(ARTIFACT_DIR)" $(RUN_FLAGS)

.PHONY: run-smoke-functions
run-smoke-functions: build ## Запускает smoke конфиг с кастомной функцией http_request (docs/examples/configs/functions-smoke.yaml)
	@[ -f $(ENV_FILE) ] || $(MAKE) init
	$(BINARY) run --config docs/examples/configs/functions-smoke.yaml --execute-step functions_smoke_init --artifact-dir "$(ARTIFACT_DIR)" $(RUN_FLAGS)
	$(MOCK_LLM_WRAPPER) $(BINARY) run --config docs/examples/configs/functions-smoke.yaml --execute-step functions_smoke_call --artifact-dir "$(ARTIFACT_DIR)" $(RUN_FLAGS)
	$(BINARY) run --config docs/examples/configs/functions-smoke.yaml --execute-step functions_smoke_verify --artifact-dir "$(ARTIFACT_DIR)" $(RUN_FLAGS)

.PHONY: run-smoke-dag-io
run-smoke-dag-io: build ## Запускает smoke конфиг DAG & I/O (docs/examples/configs/dag-and-io.yaml)
	@[ -f $(ENV_FILE) ] || $(MAKE) init
	$(MOCK_LLM_WRAPPER) $(BINARY) run --config docs/examples/configs/dag-and-io.yaml --execute-step e_llm --artifact-dir "$(ARTIFACT_DIR)" $(RUN_FLAGS)

.PHONY: run-smoke-matrix
run-smoke-matrix: build ## Запускает smoke конфиг matrix (docs/examples/configs/matrix-smoke.yaml)
	@[ -f $(ENV_FILE) ] || $(MAKE) init
	rm -rf .agent/*
	$(BINARY) run --config docs/examples/configs/matrix-smoke.yaml --execute-step run_per_item --artifact-dir "$(ARTIFACT_DIR)" --parallel 2 $(RUN_FLAGS)
	@echo "Artifacts:"
	@find .agent/artifacts/items -maxdepth 2 -type f -name 'report.txt' -print 2>/dev/null || true

.PHONY: test
test: ## Запускает go test ./...
	$(BUILD_GOFLAGS) go test $(BUILD_GOTAGS) ./...
