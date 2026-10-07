package api

import (
	"fmt"
	"os"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// TestMain lowers bcrypt's work factor for the whole package. Every handler that
// stores a password pays productionPasswordHashCost (12) on purpose -- ~0.4s of
// CPU per hash, ~2s under -race. TestTokenRoutesRequirePasswordChange,
// TestSetupToken and TestSetupDatabaseFailureDoesNotConsumeToken together spent
// 47s of this package's 55s -race run doing nothing but that arithmetic.
//
// This changes no assertion: bcrypt.CompareHashAndPassword reads the cost out of
// the hash it is given, so a hash written at MinCost verifies exactly like one
// written at 12. Only the burn is gone.
//
// Before overwriting, it checks the var still starts at the production cost:
// the overwrite hides that from every test, so this is the only place it can be
// pinned. A mismatch aborts the run.
func TestMain(m *testing.M) {
	if passwordHashCost != productionPasswordHashCost {
		fmt.Fprintf(os.Stderr, "passwordHashCost starts at %d; it must start at productionPasswordHashCost (%d)\n",
			passwordHashCost, productionPasswordHashCost)
		os.Exit(1)
	}
	passwordHashCost = bcrypt.MinCost
	os.Exit(m.Run())
}

// TestProductionPasswordHashCost pins the number the shipped binary uses. The
// var above is writable so tests can lower it; this is what stops the constant
// itself from being lowered, in a test run where the var no longer holds it.
func TestProductionPasswordHashCost(t *testing.T) {
	if productionPasswordHashCost < 12 {
		t.Fatalf("production bcrypt cost is %d; it must stay at 12 or above", productionPasswordHashCost)
	}
}
