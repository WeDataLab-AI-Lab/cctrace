package main

import (
	"os"
	"testing"
)

func TestEnvIntOpt(t *testing.T) {
	const key = "CCTRACE_TEST_ENVINTOPT"

	t.Run("unset is nil", func(t *testing.T) {
		os.Unsetenv(key)
		if got := envIntOpt(key); got != nil {
			t.Errorf("unset: got %v, want nil", got)
		}
	})
	t.Run("blank is nil", func(t *testing.T) {
		t.Setenv(key, "   ")
		if got := envIntOpt(key); got != nil {
			t.Errorf("blank: got %v, want nil", got)
		}
	})
	t.Run("valid value", func(t *testing.T) {
		t.Setenv(key, "30")
		if got := envIntOpt(key); got == nil || *got != 30 {
			t.Errorf("valid: got %v, want 30", got)
		}
	})
	t.Run("zero is a real value (not nil)", func(t *testing.T) {
		t.Setenv(key, " 0 ")
		if got := envIntOpt(key); got == nil || *got != 0 {
			t.Errorf("zero: got %v, want 0", got)
		}
	})
	t.Run("non-integer is nil", func(t *testing.T) {
		t.Setenv(key, "ninety")
		if got := envIntOpt(key); got != nil {
			t.Errorf("non-integer: got %v, want nil", got)
		}
	})
}
