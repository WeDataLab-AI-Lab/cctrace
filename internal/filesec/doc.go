// Package filesec answers one question about the client's own state files and
// directories -- can users other than the owner change them? -- as far as each
// OS lets it be answered: from the permission bits where the file system has
// them, and on Windows from the access list's write grants to Everyone,
// Authenticated Users and BUILTIN\Users. Windows grants to any other account are
// not looked for.
package filesec
