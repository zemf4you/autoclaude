package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// A thinking block is not portable. Anthropic verifies every block it is
// sent and refuses a conversation that holds one it did not write, which
// Claude Code then reports to Anthropic as a corrupted conversation. Claude
// Code leaves out the signed blocks of other models by itself, but a block
// with an empty signature, which is what many providers send, it lets
// through. Other providers do not verify.
//
// The history lives on the client, holds blocks by several authors after a
// model switch, and does not say who wrote which. So the gateway records the
// author in the block itself. A block has one field that the client must
// return exactly as it got it: the signature of a thinking block, the data
// of a redacted one. In the response of a third-party model the gateway
// wraps that field in an envelope naming the model. On the way back it opens
// the envelope: a provider gets the block as it was produced, and a block is
// left out of a request to a model that would refuse it. A block without an
// envelope is Claude's and is never changed.

// envelopePrefix starts a value wrapped by the gateway and carries the
// version of the format. No signature can be taken for an envelope: Claude's
// are base64, which has no dot.
const envelopePrefix = "autoclaude.v1."

// envelope is what the gateway puts in place of the value it wraps.
type envelope struct {
	// Model is the ID of the model that produced the block.
	Model string `json:"model"`
	// Value is what the provider sent in the field.
	Value string `json:"value"`
}

// wrap records in a value the model that produced its block.
func wrap(model, value string) string {
	payload, _ := json.Marshal(envelope{Model: model, Value: value})
	return envelopePrefix + base64.RawURLEncoding.EncodeToString(payload)
}

// unwrap returns the author of a block and the original of the value. A
// value that is not wrapped has no author to name: the block is Claude's. An
// envelope that cannot be read has no author either, and nobody takes it.
func unwrap(value string) (author, original string, wrapped bool) {
	encoded, wrapped := strings.CutPrefix(value, envelopePrefix)
	if !wrapped {
		return "", value, false
	}
	var e envelope
	if payload, err := base64.RawURLEncoding.DecodeString(encoded); err == nil {
		_ = json.Unmarshal(payload, &e)
	}
	return e.Model, e.Value, true
}

// accepts reports whether the upstream takes a thinking block: of the given
// author if the block came in an envelope, of Claude otherwise.
func (u *upstream) accepts(author string, wrapped bool) bool {
	switch {
	case !u.thirdParty:
		return !wrapped
	case u.verifies:
		return wrapped && author == u.id
	default:
		return !wrapped || author != ""
	}
}

// placeholder replaces the content of an assistant turn that is left empty
// once its thinking blocks are removed: the API rejects empty turns.
const placeholder = `[{"type":"text","text":"(no content)"}]`

func isThinking(block gjson.Result) bool {
	switch block.Get("type").String() {
	case "thinking", "redacted_thinking":
		return true
	}
	return false
}

// carrier names the field of a thinking block that holds the envelope.
func carrier(block gjson.Result) string {
	if block.Get("type").String() == "redacted_thinking" {
		return "data"
	}
	return "signature"
}

// openEnvelopes prepares the thinking blocks of a request for its upstream:
// the blocks the upstream accepts get their original values back, the others
// are removed. Everything else in the body stays byte-for-byte, and so does
// the whole body of a request that has no envelopes to open.
func openEnvelopes(body []byte, up *upstream) []byte {
	if !bytes.Contains(body, []byte(envelopePrefix)) && !(up.thirdParty && up.verifies) {
		return body
	}
	var edits []func([]byte) []byte
	messageIndex := -1
	gjson.GetBytes(body, "messages").ForEach(func(_, message gjson.Result) bool {
		messageIndex++
		content := message.Get("content")
		if message.Get("role").String() != "assistant" || !content.IsArray() {
			return true
		}
		contentPath := fmt.Sprintf("messages.%d.content", messageIndex)
		var blockEdits []func([]byte) []byte
		blocks, removed := 0, 0
		content.ForEach(func(_, block gjson.Result) bool {
			path := fmt.Sprintf("%s.%d", contentPath, blocks)
			blocks++
			if !isThinking(block) {
				return true
			}
			field := carrier(block)
			author, original, wrapped := unwrap(block.Get(field).String())
			switch {
			case !up.accepts(author, wrapped):
				removed++
				blockEdits = append(blockEdits, func(b []byte) []byte {
					b, _ = sjson.DeleteBytes(b, path)
					return b
				})
			case wrapped:
				blockEdits = append(blockEdits, func(b []byte) []byte {
					b, _ = sjson.SetBytes(b, path+"."+field, original)
					return b
				})
			}
			return true
		})
		if removed > 0 && removed == blocks {
			blockEdits = []func([]byte) []byte{func(b []byte) []byte {
				b, _ = sjson.SetRawBytes(b, contentPath, []byte(placeholder))
				return b
			}}
		}
		edits = append(edits, blockEdits...)
		return true
	})
	// Last to first, so that removing a block does not shift the indexes of
	// the edits still to be applied.
	for i := len(edits) - 1; i >= 0; i-- {
		body = edits[i](body)
	}
	return body
}
