package executor

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/zvuk/pipelineai/internal/runtime/llm"
)

func validateLLMFinalResponse(stepValidator string, resp llm.ChatCompletionResponse, messages []llm.Message, extra map[string]any) error {
	switch strings.TrimSpace(strings.ToLower(stepValidator)) {
	case "":
		return nil
	case "review_file":
		return validateReviewFileResponse(resp, messages, extra)
	default:
		return fmt.Errorf("executor: unsupported response validator %q", stepValidator)
	}
}

func validateReviewFileResponse(resp llm.ChatCompletionResponse, messages []llm.Message, extra map[string]any) error {
	finalText := ""
	if len(resp.Choices) > 0 {
		finalText = strings.TrimSpace(resp.Choices[0].Message.Content)
	}
	if finalText == "" {
		return fmt.Errorf("executor: review_file validator rejected empty response")
	}
	if strings.EqualFold(finalText, "SKIP") {
		return nil
	}

	lower := strings.ToLower(finalText)
	for _, banned := range []string{"patch applied", "applied patch", "created file", "added file"} {
		if strings.Contains(lower, banned) {
			return fmt.Errorf("executor: review_file validator rejected response containing banned phrase %q", banned)
		}
	}

	allowed := allowedReviewFilePaths(extra)
	noteCalls := 0
	successfulNotes := 0
	deduplicatedNotes := 0
	callAnchors := make(map[string]string)
	successfulAnchors := make(map[string]int)

	for _, msg := range messages {
		if msg.Role == llm.RoleAssistant {
			for _, tc := range msg.ToolCalls {
				if !isInlineNoteTool(tc.Function.Name) {
					continue
				}
				noteCalls++
				filePath, line, err := parseInlineNoteArgs(tc.Function.Arguments)
				if err != nil {
					return fmt.Errorf("executor: review_file validator rejected invalid inline note arguments: %w", err)
				}
				if len(allowed) > 0 {
					if _, ok := allowed[filePath]; !ok {
						return fmt.Errorf("executor: review_file validator rejected inline note for file %q outside of current review unit", filePath)
					}
				}
				if line <= 0 {
					return fmt.Errorf("executor: review_file validator rejected non-positive inline note line %d", line)
				}
				if strings.TrimSpace(tc.ID) != "" {
					callAnchors[strings.TrimSpace(tc.ID)] = inlineAnchorKey(filePath, line)
				}
			}
			continue
		}

		if msg.Role != llm.RoleTool || strings.TrimSpace(msg.Content) == "" {
			continue
		}
		var payload struct {
			Tool   string `json:"tool"`
			Ok     bool   `json:"ok"`
			Stdout string `json:"stdout"`
		}
		if err := json.Unmarshal([]byte(msg.Content), &payload); err != nil {
			continue
		}
		if isInlineNoteTool(payload.Tool) && payload.Ok {
			created, deduplicated := classifyInlineNoteToolResult(payload.Stdout)
			if deduplicated {
				deduplicatedNotes++
			}
			if created {
				successfulNotes++
				anchorKey := callAnchors[strings.TrimSpace(msg.ToolCallID)]
				if anchorKey != "" {
					successfulAnchors[anchorKey]++
				}
			}
		}
	}

	if noteCalls == 0 {
		return fmt.Errorf("executor: review_file validator expected at least one inline note tool call for non-SKIP response")
	}
	for anchorKey, count := range successfulAnchors {
		if count > 1 {
			return fmt.Errorf("executor: review_file validator rejected duplicate inline note creation for anchor %q", anchorKey)
		}
	}
	if successfulNotes == 0 && deduplicatedNotes == 0 {
		return fmt.Errorf("executor: review_file validator did not observe any successful or deduplicated inline note result")
	}
	return nil
}

func classifyInlineNoteToolResult(stdout string) (created bool, deduplicated bool) {
	created = true
	if strings.TrimSpace(stdout) == "" {
		return created, false
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &payload); err != nil {
		return created, false
	}
	if value, ok := payload["deduplicated"].(bool); ok && value {
		return false, true
	}
	if value, ok := payload["created"].(bool); ok && !value {
		return false, false
	}
	return created, false
}

func inlineAnchorKey(filePath string, line int) string {
	return fmt.Sprintf("%s:%d", strings.TrimSpace(filePath), line)
}

func allowedReviewFilePaths(extra map[string]any) map[string]struct{} {
	out := make(map[string]struct{})
	if len(extra) == 0 {
		return out
	}
	matrix, ok := extra["matrix"].(map[string]any)
	if !ok {
		return out
	}
	if primary := strings.TrimSpace(anyToString(matrix["file_path"])); primary != "" {
		out[primary] = struct{}{}
	}
	for _, raw := range strings.Split(anyToString(matrix["file_paths_csv"]), ",") {
		if path := strings.TrimSpace(raw); path != "" {
			out[path] = struct{}{}
		}
	}
	return out
}

func isInlineNoteTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "gitlab_create_inline_draft_note", "github_post_inline_comment":
		return true
	default:
		return false
	}
}

func parseInlineNoteArgs(raw string) (string, int, error) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &payload); err != nil {
		return "", 0, err
	}
	filePath := strings.TrimSpace(anyToString(payload["file_path"]))
	if filePath == "" {
		return "", 0, fmt.Errorf("missing file_path")
	}
	line, err := anyToInt(payload["line"])
	if err != nil {
		return "", 0, fmt.Errorf("invalid line: %w", err)
	}
	return filePath, line, nil
}

func anyToString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		return fmt.Sprint(v)
	}
}

func anyToInt(value any) (int, error) {
	switch v := value.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		return int(v), nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, err
		}
		return n, nil
	default:
		return 0, fmt.Errorf("unsupported value type %T", value)
	}
}
