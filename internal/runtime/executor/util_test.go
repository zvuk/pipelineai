package executor

import (
	"os"
	"sync"
	"testing"
)

func resetCropLimit(t *testing.T, val *string) func() {
	t.Helper()
	prev, hadPrev := os.LookupEnv("LOG_CROP_LIMIT")
	if val == nil {
		_ = os.Unsetenv("LOG_CROP_LIMIT")
	} else {
		if err := os.Setenv("LOG_CROP_LIMIT", *val); err != nil {
			t.Fatalf("failed to set LOG_CROP_LIMIT: %v", err)
		}
	}
	cropLimitOnce = sync.Once{}
	cropLimitEnv = nil
	return func() {
		if hadPrev {
			_ = os.Setenv("LOG_CROP_LIMIT", prev)
		} else {
			_ = os.Unsetenv("LOG_CROP_LIMIT")
		}
		cropLimitOnce = sync.Once{}
		cropLimitEnv = nil
	}
}

func TestCrop_DefaultLimit(t *testing.T) {
	defer resetCropLimit(t, nil)()
	text := "1234567"
	got := crop(text, 5)
	if got != "12..." {
		t.Fatalf("unexpected crop result: %q", got)
	}
}

func TestCrop_EnvOverrideApplied(t *testing.T) {
	val := "8"
	defer resetCropLimit(t, &val)()
	text := "1234567890"
	got := crop(text, 5)
	if got != "12345..." {
		t.Fatalf("env override should be used, got: %q", got)
	}
}

func TestCrop_InvalidEnvFallsBack(t *testing.T) {
	val := "abc"
	defer resetCropLimit(t, &val)()
	text := "abcdef"
	got := crop(text, 4)
	if got != "a..." {
		t.Fatalf("invalid env should fall back to default limit, got: %q", got)
	}
}
