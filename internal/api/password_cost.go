package api

// productionPasswordHashCost is bcrypt's work factor for every password this
// package stores. It is a security parameter, not a tuning knob: lowering it
// weakens every hash in the database. password_cost_test.go pins it.
const productionPasswordHashCost = 12

// passwordHashCost is what the handlers actually pass to bcrypt. It exists only
// so tests can lower it (see TestMain in password_cost_test.go). Cost 12 burns ~0.4s of
// CPU per hash by design, and the race detector multiplies that by ~5: three
// tests that each change a password accounted for 47s of internal/api's 55s
// run. Nothing outside a test may assign to it.
var passwordHashCost = productionPasswordHashCost
