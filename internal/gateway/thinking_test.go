package gateway

import (
	"slices"
	"testing"

	"github.com/tidwall/gjson"
)

// history is a conversation after three model switches: a turn by Claude,
// one by deepseek, one by qwen, and one whose envelope cannot be read.
var history = `{"model":"m","thinking":{"type":"enabled"},"messages":[
 {"role":"user","content":"hi"},
 {"role":"assistant","content":[{"type":"thinking","thinking":"a","signature":"CLAUDE"},{"type":"redacted_thinking","data":"x"},{"type":"tool_use","id":"1","name":"t","input":{}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"ok"}]},
 {"role":"assistant","content":[{"type":"thinking","thinking":"b","signature":"` + wrap("deepseek", "DS") + `"},{"type":"redacted_thinking","data":"` + wrap("deepseek", "R") + `"},{"type":"text","text":"done"}]},
 {"role":"user","content":"more"},
 {"role":"assistant","content":[{"type":"thinking","thinking":"c","signature":"` + wrap("qwen", "") + `"}]},
 {"role":"user","content":"and more"},
 {"role":"assistant","content":[{"type":"thinking","thinking":"d","signature":"` + envelopePrefix + `?"},{"type":"text","text":"end"}]}
]}`

// carried returns what the thinking blocks of a request carry in their
// signature or data, in order.
func carried(body []byte) []string {
	out := []string{}
	gjson.GetBytes(body, "messages.#.content|@flatten").ForEach(func(_, block gjson.Result) bool {
		if isThinking(block) {
			out = append(out, block.Get(carrier(block)).String())
		}
		return true
	})
	return out
}

func TestOpenEnvelopes(t *testing.T) {
	cases := []struct {
		name     string
		upstream *upstream
		carried  []string
		qwenTurn string // type of the only block of the turn by qwen
	}{
		{"Claude takes its own blocks", &upstream{}, []string{"CLAUDE", "x"}, "text"},
		{"a provider takes every block", &upstream{thirdParty: true, id: "qwen"}, []string{"CLAUDE", "x", "DS", "R", ""}, "thinking"},
		{"a provider that verifies takes its own", &upstream{thirdParty: true, id: "deepseek", verifies: true}, []string{"DS", "R"}, "text"},
	}
	for _, c := range cases {
		out := openEnvelopes([]byte(history), c.upstream)
		if !gjson.ValidBytes(out) {
			t.Fatalf("%s: invalid JSON", c.name)
		}
		if got := carried(out); !slices.Equal(got, c.carried) {
			t.Errorf("%s: blocks carry %q, want %q", c.name, got, c.carried)
		}
		if got := gjson.GetBytes(out, "messages.5.content.0.type").String(); got != c.qwenTurn {
			t.Errorf("%s: the turn by qwen holds %q, want %q", c.name, got, c.qwenTurn)
		}
		if got := gjson.GetBytes(out, `messages.1.content.#(type=="tool_use").id`).String(); got != "1" {
			t.Errorf("%s: tool_use lost", c.name)
		}
	}
}

func TestRequestWithoutEnvelopesIsUntouched(t *testing.T) {
	body := `{"messages": [ {"role":"assistant","content":[{"type":"thinking","signature":"S"}]} ]}`
	for _, up := range []*upstream{{}, {thirdParty: true, id: "qwen"}} {
		if got := openEnvelopes([]byte(body), up); string(got) != body {
			t.Errorf("third party %v: body %s", up.thirdParty, got)
		}
	}
}

func TestEnvelope(t *testing.T) {
	for _, value := range []string{"", "SIG", "openrouter.reasoning:eyJ4IjoxfQ"} {
		author, original, wrapped := unwrap(wrap("qwen3.5:4b", value))
		if author != "qwen3.5:4b" || original != value || !wrapped {
			t.Errorf("%q came back as %q by %q, wrapped %v", value, original, author, wrapped)
		}
	}
	if author, original, wrapped := unwrap("CAQSgAgK"); author != "" || original != "CAQSgAgK" || wrapped {
		t.Errorf("a signature of Claude: %q by %q, wrapped %v", original, author, wrapped)
	}
}
