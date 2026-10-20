package sessionrecord

import _ "embed"

// promptV1 is the approved, versioned instruction contract. It contains no
// examples so the instruction token cost stays bounded for short sessions.
//
//go:embed prompt_v1.txt
var promptV1 string

func Instructions() string { return promptV1 }
