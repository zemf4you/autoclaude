package gateway

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Anthropic verifies every thinking block it is sent and refuses a
// conversation that holds one it did not write, which Claude Code then
// reports to Anthropic as a corrupted conversation.
//
// Claude Code keeps the blocks of other models away from a model by itself,
// but only the signed ones: a block with an empty signature, which is what
// many providers send, it lets through. So the gateway marks the blocks of
// third-party models. A block has one field that the client returns exactly
// as it got it: the signature of a thinking block, the data of a redacted
// one. In the response of a third-party model that field gets a prefix, and
// is thereby never empty. On the way back to a provider the prefix is taken
// off again. A request for a Claude model never carries a marked block,
// whatever the client does. Claude's own blocks are never changed.

// markPrefix starts the field of a block marked by the gateway and carries
// the version of the format. No signature of Claude can be taken for a
// marked one: they are base64, which has no dot.
const markPrefix = "autoclaude.v1."

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

// markedField names the field of a thinking block that carries the mark.
func markedField(block gjson.Result) string {
	if block.Get("type").String() == "redacted_thinking" {
		return "data"
	}
	return "signature"
}

// unmark prepares the marked thinking blocks of a request for its upstream:
// a third-party provider gets them as they were produced, and Anthropic does
// not get them at all. Everything else in the body stays byte-for-byte, and
// so does the whole body of a request without marked blocks.
func unmark(body []byte, thirdParty bool) []byte {
	if !bytes.Contains(body, []byte(markPrefix)) {
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
			field := path + "." + markedField(block)
			original, marked := strings.CutPrefix(block.Get(markedField(block)).String(), markPrefix)
			switch {
			case !marked:
			case thirdParty:
				blockEdits = append(blockEdits, func(b []byte) []byte {
					b, _ = sjson.SetBytes(b, field, original)
					return b
				})
			default:
				removed++
				blockEdits = append(blockEdits, func(b []byte) []byte {
					b, _ = sjson.DeleteBytes(b, path)
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
