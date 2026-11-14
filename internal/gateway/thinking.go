package gateway

import (
	"bytes"
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

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

// native keeps only the thinking blocks produced by owner (the empty string
// is Claude) and restores their original signatures. It reports whether the
// history held blocks of other models.
func native(body []byte, owner string) (out []byte, mixed bool) {
	if owner == "" && !bytes.Contains(body, []byte(markPrefix)) {
		return body, false // a request to Claude with no foreign blocks stays byte-for-byte
	}
	out, removed := filterThinking(body, func(block gjson.Result) (bool, string) {
		model, original := author(block.Get("signature").String())
		return model == owner, original
	})
	return out, removed > 0
}

// withoutThinking returns the conservative form of a request: no thinking
// blocks and thinking disabled. A provider accepts it whatever the history.
// It reports false when the request is in that form already.
func withoutThinking(body []byte) ([]byte, bool) {
	out, removed := filterThinking(body, func(gjson.Result) (bool, string) { return false, "" })
	if mode := gjson.GetBytes(body, "thinking.type"); removed == 0 && (!mode.Exists() || mode.String() == "disabled") {
		return nil, false
	}
	out, _ = sjson.SetRawBytes(out, "thinking", []byte(`{"type":"disabled"}`))
	out, _ = sjson.DeleteBytes(out, "context_management") // may hold thinking edits, which require thinking
	return out, true
}

// filterThinking calls keep for every thinking block in assistant turns.
// Blocks keep rejects are removed; the others get the signature it returns.
// Everything else in the body stays byte-for-byte.
func filterThinking(body []byte, keep func(block gjson.Result) (ok bool, signature string)) (out []byte, removed int) {
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
		blocks, dropped := 0, 0
		content.ForEach(func(_, block gjson.Result) bool {
			path := fmt.Sprintf("%s.%d", contentPath, blocks)
			blocks++
			if !isThinking(block) {
				return true
			}
			ok, signature := keep(block)
			switch {
			case !ok:
				dropped++
				blockEdits = append(blockEdits, func(b []byte) []byte {
					b, _ = sjson.DeleteBytes(b, path)
					return b
				})
			case signature != block.Get("signature").String():
				blockEdits = append(blockEdits, func(b []byte) []byte {
					b, _ = sjson.SetBytes(b, path+".signature", signature)
					return b
				})
			}
			return true
		})
		removed += dropped
		if dropped > 0 && dropped == blocks {
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
	out = body
	for i := len(edits) - 1; i >= 0; i-- {
		out = edits[i](out)
	}
	return out, removed
}
