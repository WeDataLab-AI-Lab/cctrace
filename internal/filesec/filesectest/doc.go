// Package filesectest builds the access lists the tests of filesec and of its
// callers need. It exists only on Windows, where a file's access list is the one
// thing that makes it writable by others, and is imported by test files alone.
package filesectest
