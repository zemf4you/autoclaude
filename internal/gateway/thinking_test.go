package gateway

import (
	"slices"
	"testing"

	"github.com/tidwall/gjson"
)

// history is a conversation after two model switches: a turn by Claude, one
// by a provider's model that signs its thinking, and one by a model that
// does not.
const history = `{"model":"m","thinking":{"type":"enabled"},"messages":[
 {"role":"user","content":"hi"},
 {"role":"assistant","content":[{"type":"thinking","thinking":"a","signature":"CLAUDE"},{"type":"redacted_thinking","data":"x"},{"type":"tool_use","id":"1","name":"t","input":{}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"ok"}]},
 {"role":"assistant","content":[{"type":"thinking","thinking":"b","signature":"` + markPrefix + `SIG"},{"type":"redacted_thinking","data":"` + markPrefix + `DATA"},{"type":"text","text":"done"}]},
 {"role":"user","content":"more"},
 {"role":"assistant","content":[{"type":"thinking","thinking":"c","signature":"` + markPrefix + `"}]}
]}`

// marked returns what the thinking blocks of a request hold in the field
// that carries the mark, in order.
func marked(body []byte) []string {
	out := []string{}
	gjson.GetBytes(body, "messages.#.content|@flatten").ForEach(func(_, block gjson.Result) bool {
		if isThinking(block) {
			out = append(out, block.Get(markedField(block)).String())
		}
		return true
	})
	return out
}

func TestUnmark(t *testing.T) {
	cases := []struct {
		name       string
		thirdParty bool
		blocks     []string
		lastTurn   string // type of the only block of the last turn
	}{
		{"Claude gets its own blocks", false, []string{"CLAUDE", "x"}, "text"},
		{"a provider gets every block as it was produced", true, []string{"CLAUDE", "x", "SIG", "DATA", ""}, "thinking"},
	}
	for _, c := range cases {
		out := unmark([]byte(history), c.thirdParty)
		if !gjson.ValidBytes(out) {
			t.Fatalf("%s: invalid JSON", c.name)
		}
		if got := marked(out); !slices.Equal(got, c.blocks) {
			t.Errorf("%s: blocks hold %q, want %q", c.name, got, c.blocks)
		}
		if got := gjson.GetBytes(out, "messages.5.content.0.type").String(); got != c.lastTurn {
			t.Errorf("%s: the last turn holds %q, want %q", c.name, got, c.lastTurn)
		}
		if got := gjson.GetBytes(out, `messages.1.content.#(type=="tool_use").id`).String(); got != "1" {
			t.Errorf("%s: tool_use lost", c.name)
		}
	}
}

func TestRequestWithoutMarksIsUntouched(t *testing.T) {
	body := `{"messages": [ {"role":"assistant","content":[{"type":"thinking","signature":"S"}]} ]}`
	for _, thirdParty := range []bool{false, true} {
		if got := unmark([]byte(body), thirdParty); string(got) != body {
			t.Errorf("third party %v: body %s", thirdParty, got)
		}
	}
}
