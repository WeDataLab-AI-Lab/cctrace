package containertest

import "testing"

func TestRequired(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "unset", value: "", want: false},
		{name: "one", value: "1", want: true},
		// Only "1" opts in: a stray "0" or "false" from a shell profile must not
		// turn every developer's `go test ./...` into a hard failure.
		{name: "zero", value: "0", want: false},
		{name: "true", value: "true", want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(RequireEnv, c.value)
			if got := Required(); got != c.want {
				t.Errorf("Required() with %s=%q = %v, want %v", RequireEnv, c.value, got, c.want)
			}
		})
	}
}
