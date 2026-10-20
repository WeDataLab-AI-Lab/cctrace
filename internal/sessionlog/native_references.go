package sessionlog

import (
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"encoding/json"
	"hash"

	"cctrace/internal/sessiontrace"
)

const maxNativeReferenceSources = 100

type nativeBlockReference struct {
	Index int    `json:"index"`
	Hash  string `json:"hash"`
}

type nativeSourceReference struct {
	UUID       string `json:"uuid"`
	BlockStart int    `json:"block_start"`
	BlockEnd   int    `json:"block_end"`
}

type nativeMessageReference struct {
	Hash    string                  `json:"hash"`
	Blocks  []nativeBlockReference  `json:"blocks,omitempty"`
	Records []nativeSourceReference `json:"records,omitempty"`
}

type nativeReferenceIndex struct {
	Version          int                      `json:"version"`
	Messages         []nativeMessageReference `json:"messages"`
	PrefixIncomplete bool                     `json:"prefix_incomplete,omitempty"`
}

// ReferenceCollector keeps native transcript prefix hashes for one selected
// scan. Its rolling digest does not accumulate message bodies. State is deliberately
// not persisted: a hash-state encoding may contain plaintext pending bytes.
// A resumed scan marks its first group incomplete instead of reading backward
// across collection consent or pretending an unseen prefix is empty.
type ReferenceCollector struct {
	digest                     hash.Hash
	role, messageID, sessionID string
	sources                    []nativeSourceReference
	blockCount                 int
	incomplete                 bool
	lastOffset                 int64
	lastLineBytes              int
	seenLine                   bool
}

func NewReferenceCollector(fromOffset int64) *ReferenceCollector {
	return &ReferenceCollector{incomplete: fromOffset > 0}
}

// Reset starts an unknown prefix after a source gap, exclusion or scope change.
func (c *ReferenceCollector) Reset() {
	c.digest = nil
	c.sources = nil
	c.blockCount = 0
	c.incomplete = true
}

func (c *ReferenceCollector) Capture(r *Record) {
	// Metadata/system/attachment lines still pass through this method. Only
	// bytes that were actually skipped or excluded break the transcript group.
	if c.seenLine && r.Offset > c.lastOffset+int64(c.lastLineBytes)+2 {
		c.Reset()
	}
	c.lastOffset, c.lastLineBytes, c.seenLine = r.Offset, len(r.RawLine), true
	if r.Type != "user" && r.Type != "assistant" {
		return
	}
	message, err := sessiontrace.DecodeObject(r.Message)
	if err != nil {
		c.Reset()
		return
	}
	normalized, err := sessiontrace.NormalizeMessage(message)
	if err != nil {
		c.Reset()
		return
	}
	role := normalized["role"].(string)
	messageID, _ := message["id"].(string)
	merge := c.digest != nil && c.role == role && c.sessionID == r.SessionID && (role == "user" || (role == "assistant" && messageID != "" && messageID == c.messageID))
	if !merge {
		if c.digest != nil {
			c.incomplete = false
		} // A new observed group has a known start.
		c.digest = sha256.New()
		_, _ = c.digest.Write([]byte(`{"content":[`))
		c.sources = nil
		c.blockCount = 0
	} else if len(c.sources) >= maxNativeReferenceSources {
		// Bound source metadata on a very long same-role group. A cut is explicit;
		// it must never masquerade as a complete native message reference.
		c.Reset()
		c.digest = sha256.New()
		_, _ = c.digest.Write([]byte(`{"content":[`))
	}
	c.role, c.messageID, c.sessionID = role, messageID, r.SessionID
	content := normalized["content"].([]any)
	blocks := make([]nativeBlockReference, 0, len(content))
	for i, block := range content {
		encoded, err := sessiontrace.CanonicalJSON(block)
		if err != nil {
			c.Reset()
			return
		}
		blockHash := sha256.Sum256(encoded)
		blocks = append(blocks, nativeBlockReference{Index: i, Hash: hex.EncodeToString(blockHash[:])})
		if c.blockCount > 0 {
			_, _ = c.digest.Write([]byte(","))
		}
		_, _ = c.digest.Write(encoded)
		c.blockCount++
	}
	rowHash, err := sessiontrace.CanonicalHash(normalized)
	if err != nil {
		c.Reset()
		return
	}
	source := nativeSourceReference{UUID: referenceUUID(r), BlockEnd: len(content)}
	rowSources := []nativeSourceReference(nil)
	if source.UUID != "" {
		rowSources = []nativeSourceReference{source}
		c.sources = append(c.sources, source)
	}
	index := nativeReferenceIndex{Version: 1, PrefixIncomplete: c.incomplete, Messages: []nativeMessageReference{{Hash: rowHash, Blocks: blocks, Records: rowSources}}}
	if len(c.sources) > 1 {
		prefixHash, err := c.prefixHash(role)
		if err != nil {
			c.Reset()
			return
		}
		index.Messages = append(index.Messages, nativeMessageReference{Hash: prefixHash, Records: append([]nativeSourceReference(nil), c.sources...)})
	}
	encoded, err := json.Marshal(index)
	if err == nil {
		r.CctraceReferences = derivedJSON(encoded)
	}
	if source.UUID == "" {
		c.Reset()
	}
}

func (c *ReferenceCollector) prefixHash(role string) (string, error) {
	// Clone only in memory, append the canonical suffix to the clone, and keep
	// the original digest ready for another observed content block.
	state, err := c.digest.(encoding.BinaryMarshaler).MarshalBinary()
	if err != nil {
		return "", err
	}
	copy := sha256.New()
	if err := copy.(encoding.BinaryUnmarshaler).UnmarshalBinary(state); err != nil {
		return "", err
	}
	encodedRole, err := sessiontrace.CanonicalJSON(role)
	if err != nil {
		return "", err
	}
	_, _ = copy.Write([]byte(`],"role":`))
	_, _ = copy.Write(encodedRole)
	_, _ = copy.Write([]byte("}"))
	return hex.EncodeToString(copy.Sum(nil)), nil
}

func referenceUUID(r *Record) string {
	if r.UUID != "" {
		return r.UUID
	}
	if len(r.RawLine) == 0 {
		return ""
	}
	digest := sha256.Sum256(append([]byte(r.SessionID+"\x00"), r.RawLine...))
	return "content-" + hex.EncodeToString(digest[:16])
}

// CaptureReferences adds an independent row index for direct conversions.
// A scan collector's richer prefix metadata is retained when already present.
func (r *Record) CaptureReferences() {
	if len(r.CctraceReferences) == 0 {
		NewReferenceCollector(0).Capture(r)
	}
}
