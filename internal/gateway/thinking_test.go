package gateway

import (
	"slices"
	"testing"

	"github.com/tidwall/gjson"
)

// history is a conversation after two model switches: a turn by Claude,
// one by deepseek, one by qwen.
const history = `{"model":"m","thinking":{"type":"enabled"},"context_management":{},"messages":[
 {"role":"user","content":"hi"},
 {"role":"assistant","content":[{"type":"thinking","thinking":"a","signature":"CLAUDE"},{"type":"redacted_thinking","data":"x"},{"type":"tool_use","id":"1","name":"t","input":{}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"ok"}]},
 {"role":"assistant","content":[{"type":"thinking","thinking":"b","signature":"autoclaude deepseek DS"},{"type":"text","text":"done"}]},
 {"role":"user","content":"more"},
 {"role":"assistant","content":[{"type":"thinking","thinking":"c","signature":"autoclaude qwen "}]}
]}`

func signatures(body []byte) []string {
	var out []string
	for _, s := range gjson.GetBytes(body, `messages.#.content.#(type=="thinking")#.signature|@flatten`).Array() {
		out = append(out, s.String())
	}
	return out
}

func TestNative(t *testing.T) {
	cases := []struct {
		owner      string
		signatures []string
		redacted   int64
		last       string // type of the only block of the last turn
	}{
		{"", []string{"CLAUDE"}, 1, "text"},
		{"deepseek", []string{"DS"}, 0, "text"},
		{"qwen", []string{""}, 0, "thinking"},
	}
	for _, c := range cases {
		out, mixed := native([]byte(history), c.owner)
		if !mixed {
			t.Errorf("owner %q: the history is mixed", c.owner)
		}
		if !gjson.ValidBytes(out) {
			t.Fatalf("owner %q: invalid JSON", c.owner)
		}
		if got := signatures(out); !slices.Equal(got, c.signatures) {
			t.Errorf("owner %q: signatures = %q, want %q", c.owner, got, c.signatures)
		}
		if got := gjson.GetBytes(out, `messages.1.content.#(type=="redacted_thinking")#|#`).Int(); got != c.redacted {
			t.Errorf("owner %q: redacted_thinking blocks = %d, want %d", c.owner, got, c.redacted)
		}
		if got := gjson.GetBytes(out, "messages.5.content.0.type").String(); got != c.last {
			t.Errorf("owner %q: last turn = %q, want %q", c.owner, got, c.last)
		}
		if got := gjson.GetBytes(out, `messages.1.content.#(type=="tool_use").id`).String(); got != "1" {
			t.Errorf("owner %q: tool_use lost", c.owner)
		}
	}
}

func TestNativeLeavesClaudeRequestUntouched(t *testing.T) {
	body := `{"messages": [ {"role":"assistant","content":[{"type":"thinking","signature":"S"}]} ]}`
	if got, mixed := native([]byte(body), ""); string(got) != body || mixed {
		t.Errorf("mixed = %v, body: %s", mixed, got)
	}
}

func TestWithoutThinking(t *testing.T) {
	out, ok := withoutThinking([]byte(history))
	if !ok || !gjson.ValidBytes(out) {
		t.Fatalf("ok = %v, body: %s", ok, out)
	}
	if got := signatures(out); len(got) != 0 {
		t.Errorf("thinking blocks left: %q", got)
	}
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "disabled" {
		t.Errorf("thinking.type = %q", got)
	}
	if gjson.GetBytes(out, "context_management").Exists() {
		t.Error("context_management not removed")
	}
	if _, ok := withoutThinking([]byte(`{"messages":[{"role":"user","content":"hi"}]}`)); ok {
		t.Error("a request without thinking is conservative already")
	}
	// No blocks left in the history, but thinking is still requested.
	out, ok = withoutThinking([]byte(`{"thinking":{"type":"adaptive"},"messages":[]}`))
	if !ok || gjson.GetBytes(out, "thinking.type").String() != "disabled" {
		t.Errorf("ok = %v, body: %s", ok, out)
	}
}
