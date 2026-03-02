package dsl

// В этом файле описываются основные модели YAML-конфигурации PipelineAI.

// Config описывает корневую структуру .agents.yaml.
type Config struct {
	Version   int        `yaml:"version"`
	Agent     Agent      `yaml:"agent"`
	Defaults  *Defaults  `yaml:"defaults,omitempty"`
	Functions []Function `yaml:"functions,omitempty"`
	Steps     []Step     `yaml:"steps"`
	// Approvers — глобальные правила для инструментов (shell/apply_patch).
	Approvers []Approver `yaml:"approvers,omitempty"`
	// BaseDir — директория файла конфигурации (служебное поле, не из YAML)
	BaseDir string `yaml:"-"`
}

// Agent содержит глобальные настройки агента.
type Agent struct {
	Name        string      `yaml:"name"`
	Model       string      `yaml:"model"`
	ArtifactDir string      `yaml:"artifact_dir"`
	OpenAI      AgentOpenAI `yaml:"openai"`
	// Reasoning включает передачу и сохранение reasoning, если модель возвращает его (по умолчанию false)
	Reasoning bool `yaml:"reasoning,omitempty"`
}

// AgentOpenAI описывает параметры подключения к OpenAI-совместимому API.
type AgentOpenAI struct {
	BaseURL   string `yaml:"base_url"`
	APIKeyEnv string `yaml:"api_key_env"`
}

// Defaults задаёт значения по умолчанию для шагов.
type Defaults struct {
	// StepTimeout — таймаут одного шага по умолчанию.
	StepTimeout *Duration `yaml:"step_timeout,omitempty"`
	// ScenarioTimeout — таймаут всего сценария (run) по умолчанию.
	ScenarioTimeout *Duration         `yaml:"scenario_timeout,omitempty"`
	Env             map[string]string `yaml:"env,omitempty"`
	// ToolTimeout — таймаут выполнения инструментов по умолчанию (перекрывается на шаге).
	ToolTimeout *Duration `yaml:"tool_timeout,omitempty"`
}

// Function описывает пользовательскую функцию, доступную шагам type: llm.
type Function struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	// Prompt — системная инструкция для LLM о корректном использовании функции
	Prompt         TemplateString         `yaml:"prompt,omitempty"`
	Parameters     map[string]any         `yaml:"parameters,omitempty"`
	Implementation FunctionImplementation `yaml:"implementation"`
}

// FunctionImplementation описывает реализацию пользовательской функции.
type FunctionImplementation struct {
	Type          string         `yaml:"type"`
	ShellTemplate TemplateString `yaml:"shell_template"`
}

// Step описывает универсальный шаг. В рамках задач поддерживаем type: llm.
type Step struct {
	ID          string            `yaml:"id"`
	Type        string            `yaml:"type"`
	Name        TemplateString    `yaml:"name,omitempty"`
	Description TemplateString    `yaml:"description,omitempty"`
	Template    bool              `yaml:"template,omitempty"`
	Needs       []string          `yaml:"needs,omitempty"`
	Timeout     *Duration         `yaml:"timeout,omitempty"`
	Env         map[string]string `yaml:"env,omitempty"`
	// AllowFailure разрешает пометить шаг как "необязательный":
	// при ошибке после всех попыток сценарий продолжит выполнение.
	AllowFailure bool `yaml:"allow_failure,omitempty"`
	// Retries задаёт количество попыток выполнения шага.
	// Если 0 или отрицательное — шаг исполняется один раз.
	Retries int `yaml:"retries,omitempty"`

	LLM *StepLLM `yaml:",inline"`

	// Matrix — конфигурация шага type: matrix
	Matrix *StepMatrix `yaml:"matrix,omitempty"`
	// Run — ссылка на шаблонный шаг, который исполняется для каждого элемента матрицы
	Run *StepMatrixRun `yaml:"run,omitempty"`

	// Approvers — переопределяют глобальные правила на уровне шага.
	Approvers []Approver `yaml:"approvers,omitempty"`
	// ToolTimeout — таймаут инструментов на уровне шага (перекрывает defaults.tool_timeout).
	ToolTimeout *Duration `yaml:"tool_timeout,omitempty"`

	// Inputs — декларация входов шага (пути к файлам или инлайн-шаблоны).
	Inputs []StepInput `yaml:"inputs,omitempty"`

	// Outputs — декларация выходов шага (унифицировано для llm и shell).
	Outputs []StepOutput `yaml:"outputs,omitempty"`

	// TODO: перепроверить на этапе выполнения GO-1471
	// Shell — конфигурация шага type: shell
	Shell *StepShell `yaml:"shell,omitempty"`

	// Plan — конфигурация шага type: plan.
	// Предназначен для этапов предварительного планирования работ (например, построение манифеста юнитов).
	Plan *StepPlan `yaml:"plan,omitempty"`
}

// StepLLM описывает параметры шага type: llm.
type StepLLM struct {
	SystemPrompt TemplateString `yaml:"system_prompt"`
	UserPrompt   TemplateString `yaml:"user_prompt"`
	Temperature  *float32       `yaml:"temperature,omitempty"`
	// MaxTokens задаёт верхнюю границу для токенов завершения модели.
	// Если не задано или <= 0 — ограничение не пробрасывается в запрос.
	MaxTokens    *int         `yaml:"max_tokens,omitempty"`
	ToolsAllowed []string     `yaml:"tools_allowed,omitempty"`
	Context      *StepContext `yaml:"context,omitempty"`
	// Пути к файлам с промптами (альтернатива inline-полям)
	SystemPromptPath TemplateString `yaml:"system_prompt_path,omitempty"`
	UserPromptPath   TemplateString `yaml:"user_prompt_path,omitempty"`
}

// StepShell описывает параметры шага type: shell.
// TODO: перепроверить на этапе выполнения GO-1471
type StepShell struct {
	Run     TemplateString `yaml:"run"`
	Dir     TemplateString `yaml:"dir,omitempty"`
	Timeout *Duration      `yaml:"timeout,omitempty"`
}

// StepPlan описывает параметры шага type: plan.
// По контракту он аналогичен shell-шагу, но семантически используется для вычисления стратегии выполнения.
type StepPlan struct {
	Run     TemplateString `yaml:"run"`
	Dir     TemplateString `yaml:"dir,omitempty"`
	Timeout *Duration      `yaml:"timeout,omitempty"`
}

// StepMatrix описывает параметры шага type: matrix.
type StepMatrix struct {
	// FromYAML — путь к файлу manifest (yaml/json)
	FromYAML TemplateString `yaml:"from_yaml"`
	// Select — путь внутри manifest до массива элементов (например, "items" или "services.backend")
	Select string `yaml:"select"`
	// ItemID — шаблон формирования идентификатора элемента (доступен контекст .item)
	ItemID TemplateString `yaml:"item_id"`
	// Inject — дополнительные значения, которые будут проброшены как .matrix.<key> в дочерний шаг
	Inject map[string]TemplateString `yaml:"inject,omitempty"`
}

// StepMatrixRun указывает идентификатор шага-шаблона, который надо выполнить для каждого элемента матрицы.
type StepMatrixRun struct {
	Step string `yaml:"step"`
}

// Approver описывает настройки ограничений для инструмента.
// В зависимости от значения Tool структура rules ожидает разный формат.
//   - Tool: "shell" — rules: список ShellRule
//   - Tool: "apply_patch" — rules: список ApplyPatchRule
type Approver struct {
	Tool  string `yaml:"tool"`
	Rules any    `yaml:"rules,omitempty"`
}

// ShellRule описывает запрет команды по регэкспу.
type ShellRule struct {
	// Regex — регулярное выражение для сравнения со строкой команды (как в терминале).
	Regex TemplateString `yaml:"regex"`
	// Message — сообщение на английском для модели, которое будет подставлено вместо результата инструмента.
	Message TemplateString `yaml:"message"`
}

// ApplyPatchRule описывает правило правок по файловым маскам.
type ApplyPatchRule struct {
	GlobPatterns []string `yaml:"glob_patterns"`
	AllowCreate  *bool    `yaml:"allow_create,omitempty"`
	AllowUpdate  *bool    `yaml:"allow_update,omitempty"`
	AllowDelete  *bool    `yaml:"allow_delete,omitempty"`
}

// StepContext описывает файлы и шаблоны, подключаемые к промпту шага.
type StepContext struct {
	Include []TemplateString `yaml:"include,omitempty"`
	Exclude []TemplateString `yaml:"exclude,omitempty"`
	Inline  []TemplateString `yaml:"inline,omitempty"`
}

// StepOutput описывает артефакты, которые формирует шаг.
type StepOutput struct {
	ID     string         `yaml:"id"`
	Type   string         `yaml:"type"`
	From   string         `yaml:"from"`
	Path   TemplateString `yaml:"path,omitempty"`
	Format string         `yaml:"format,omitempty"`
}

// StepInput описывает входные данные шага.
// Поддерживаемые типы: "file" (обязателен path), "inline" (обязателен template).
type StepInput struct {
	ID       string         `yaml:"id"`
	Type     string         `yaml:"type"`
	Path     TemplateString `yaml:"path,omitempty"`
	Template TemplateString `yaml:"template,omitempty"`
}
