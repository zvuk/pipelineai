package executor

import (
	"os"
	"strconv"
	"strings"
	"sync"
)

var (
	cropLimitOnce sync.Once
	cropLimitEnv  *int
)

// crop обрезает строку до n символов (или LOG_CROP_LIMIT, если выставлен) с троеточием в конце при необходимости.
func crop(s string, n int) string {
	limit := resolveCropLimit(n)
	if limit <= 0 || len(s) <= limit {
		return s
	}
	if limit > 3 {
		return s[:limit-3] + "..."
	}
	return s[:limit]
}

// resolveCropLimit читает лимит из окружения один раз и возвращает дефолтное значение при ошибке парсинга.
func resolveCropLimit(defaultLimit int) int {
	cropLimitOnce.Do(func() {
		raw := strings.TrimSpace(os.Getenv("LOG_CROP_LIMIT"))
		if raw == "" {
			return
		}
		if val, err := strconv.Atoi(raw); err == nil {
			cropLimitEnv = &val
		}
	})
	if cropLimitEnv != nil {
		return *cropLimitEnv
	}
	return defaultLimit
}
