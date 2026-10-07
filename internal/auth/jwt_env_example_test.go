package auth

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// TestEnvExampleJWTSecretRejectedByServer guards against deploy/.env.example
// shipping a JWT_SECRET value that the server would actually accept. If a
// deployment copies .env.example to .env and forgets to replace the secret,
// NewJWTManager must reject it -- otherwise the (public, mirrored) example
// value becomes a usable signing key for forging admin cookies.
func TestEnvExampleJWTSecretRejectedByServer(t *testing.T) {
	t.Parallel()

	const path = "../../deploy/.env.example"

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	var found []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "JWT_SECRET=") {
			found = append(found, strings.TrimPrefix(line, "JWT_SECRET="))
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}

	if len(found) != 1 {
		t.Fatalf("%s: expected exactly one JWT_SECRET= line, found %d", path, len(found))
	}

	value := found[0]
	if _, err := NewJWTManager(value); err == nil {
		t.Fatalf("%s: JWT_SECRET=%q passes NewJWTManager validation; it must be left empty or use a value the server rejects", path, value)
	}
}
