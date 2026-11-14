package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/zemf4you/autoclaude/internal/config"
)

// fake is a stand-in provider: it records requests and answers with reply.
type fake struct {
	*httptest.Server
	reply func(w http.ResponseWriter, body string)

	mu      sync.Mutex
	headers []http.Header
	paths   []string
	bodies  []string
}

func newFake(t *testing.T) *fake {
	f := &fake{reply: func(w http.ResponseWriter, _ string) { io.WriteString(w, `{}`) }}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.headers = append(f.headers, r.Header)
		f.paths = append(f.paths, r.URL.RequestURI())
		f.bodies = append(f.bodies, string(body))
		f.mu.Unlock()
		f.reply(w, string(body))
	}))
	t.Cleanup(f.Close)
	return f
}

// requests returns the bodies of the requests received so far.
func (f *fake) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.bodies)
}

// setup starts a gateway in front of a fake Anthropic and a fake provider
// that serves "qwen" and, under the name "org/qwen-4b", "claude-haiku".
func setup(t *testing.T) (gatewayURL string, claude, provider *fake) {
	claude, provider = newFake(t), newFake(t)
	g := New(&config.Config{
		Providers: map[string]config.Provider{"local": {BaseURL: provider.URL + "/base", APIKey: "provider-key"}},
		Models: []config.Model{
			{ID: "qwen", Provider: "local", Upstream: "qwen"},
			{ID: "claude-haiku", Provider: "local", Upstream: "org/qwen-4b"},
		},
	})
	g.anthropic.url = baseURL(claude.URL)
	server := httptest.NewServer(&httputil.ReverseProxy{
		Rewrite: func(*httputil.ProxyRequest) {}, Transport: g, FlushInterval: -1,
	})
	t.Cleanup(server.Close)
	return server.URL, claude, provider
}

func post(t *testing.T, url, body string) (int, string) {
	req, _ := http.NewRequest(http.MethodPost, url+"/v1/messages?beta=true", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer subscription-token")
	req.Header.Set("Anthropic-Beta", "oauth")
	req.Header.Set("X-Claude-Code-Session-Id", "session")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

// Turns of a conversation, as they appear in the messages of a request.
const (
	claudeTurn   = `{"role":"assistant","content":[{"type":"thinking","signature":"CLAUDE"},{"type":"tool_use","id":"1"}]}`
	qwenTurn     = `{"role":"assistant","content":[{"type":"thinking","signature":"autoclaude qwen Q"},{"type":"tool_use","id":"1"}]}`
	toolResult   = `{"role":"user","content":[{"type":"tool_result","tool_use_id":"1"}]}`
	userQuestion = `{"role":"user","content":"next"}`
)

func request(model string, messages ...string) string {
	return `{"model":"` + model + `","thinking":{"type":"adaptive"},"messages":[` + strings.Join(messages, ",") + `]}`
}

// rejectThinking makes a fake refuse every request that has thinking enabled.
func rejectThinking(f *fake) {
	f.reply = func(w http.ResponseWriter, body string) {
		if gjson.Get(body, "thinking.type").String() != "disabled" {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, "rejected")
			return
		}
		io.WriteString(w, "accepted")
	}
}

func TestClaudeRequestPassesUntouched(t *testing.T) {
	gw, claude, provider := setup(t)
	body := `{"model": "claude-fable-5-2",  "messages": [` + claudeTurn + `]}`
	post(t, gw, body)

	if got := claude.requests(); len(provider.requests()) != 0 || len(got) != 1 || got[0] != body {
		t.Fatalf("claude got %q, provider got %q", got, provider.requests())
	}
	if claude.paths[0] != "/v1/messages?beta=true" {
		t.Errorf("path = %q", claude.paths[0])
	}
	if h := claude.headers[0]; h.Get("Authorization") != "Bearer subscription-token" || h.Get("X-Claude-Code-Session-Id") != "session" {
		t.Errorf("headers changed: %v", h)
	}
}

func TestClaudeErrorPassesUntouched(t *testing.T) {
	gw, claude, _ := setup(t)
	rejectThinking(claude)
	status, out := post(t, gw, request("claude", claudeTurn, toolResult))
	if status != http.StatusBadRequest || out != "rejected" || len(claude.requests()) != 1 {
		t.Errorf("status %d, response %q, requests %d", status, out, len(claude.requests()))
	}
}

func TestProviderRequest(t *testing.T) {
	gw, _, provider := setup(t)
	post(t, gw, `{"model":"claude-haiku","thread":{"type":"create"},"diagnostics":{},"metadata":{"user_id":"u"},`+
		`"system":[{"type":"text","text":"x-anthropic-billing-header: cch=1a2b3;"},{"type":"text","text":"You are an agent."}],`+
		`"messages":[{"role":"user","content":"hi"},{"role":"system","content":"env"}]}`)

	want := `{"model":"org/qwen-4b","system":[{"type":"text","text":"You are an agent."}],` +
		`"messages":[{"role":"user","content":"hi"},{"role":"user","content":"env"}]}`
	if got := provider.requests()[0]; got != want {
		t.Errorf("provider got %s\nwant         %s", got, want)
	}
	if provider.paths[0] != "/base/v1/messages?beta=true" {
		t.Errorf("path = %q", provider.paths[0])
	}
	h := provider.headers[0]
	if h.Get("Authorization") != "Bearer provider-key" || h.Get("Anthropic-Beta") != "oauth" {
		t.Errorf("headers = %v", h)
	}
	if h.Get("X-Claude-Code-Session-Id") != "" {
		t.Errorf("provider got client identifiers: %v", h)
	}
}

func TestProviderRequestMustCarryWholeHistory(t *testing.T) {
	gw, _, provider := setup(t)
	status, out := post(t, gw, `{"model":"qwen","thread":{"type":"continue","previous_message_id":"m"},"messages":[]}`)
	if status != http.StatusBadRequest || !strings.Contains(out, "thread:") || len(provider.requests()) != 0 {
		t.Errorf("status %d, response %q, provider requests %d", status, out, len(provider.requests()))
	}
}

func TestProviderThinkingIsMarked(t *testing.T) {
	gw, _, provider := setup(t)

	provider.reply = func(w http.ResponseWriter, _ string) {
		io.WriteString(w, `{"content":[{"type":"thinking","thinking":"a","signature":"S"},{"type":"text","text":"b"}]}`)
	}
	_, out := post(t, gw, `{"model":"claude-haiku","messages":[]}`)
	// The author is the model the user picked, not the provider's name for it.
	if got := gjson.Get(out, "content.0.signature").String(); got != "autoclaude claude-haiku S" {
		t.Errorf("response signature = %q", got)
	}

	// Stream: a signature in two parts, no signature at all, a text block.
	provider.reply = func(w http.ResponseWriter, _ string) {
		for _, data := range []string{
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"S"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"IG"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":""}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_stop","index":2}`,
		} {
			io.WriteString(w, "event: "+gjson.Get(data, "type").String()+"\r\ndata: "+data+"\r\n\r\n")
		}
	}
	_, out = post(t, gw, `{"model":"qwen","stream":true,"messages":[]}`)
	var got []string
	for _, event := range strings.Split(strings.TrimSpace(strings.ReplaceAll(out, "\r\n", "\n")), "\n\n") {
		name, data, _ := strings.Cut(event, "\n")
		data = strings.TrimPrefix(data, "data: ")
		if name != "event: "+gjson.Get(data, "type").String() {
			t.Errorf("event name disagrees with its data: %q", event)
		}
		step := gjson.Get(data, "type").String() + " " + gjson.Get(data, "index").String()
		if signature := gjson.Get(data, "delta.signature"); signature.Exists() {
			step += " " + signature.String()
		}
		got = append(got, step)
	}
	want := []string{
		"content_block_start 0", "content_block_delta 0 autoclaude qwen SIG", "content_block_stop 0",
		"content_block_start 1", "content_block_delta 1 autoclaude qwen ", "content_block_stop 1",
		"content_block_start 2", "content_block_stop 2",
	}
	if !slices.Equal(got, want) {
		t.Errorf("stream =\n%q\nwant\n%q", got, want)
	}
}

// A switch in the middle of a tool loop leaves the new model with a history
// it may refuse. The request then goes through without thinking.
func TestRejectedHistoryIsRetriedWithoutThinking(t *testing.T) {
	gw, claude, provider := setup(t)
	rejectThinking(claude)
	rejectThinking(provider)

	for _, model := range []string{"claude", "qwen"} {
		foreignTurn, upstream := qwenTurn, claude
		if model == "qwen" {
			foreignTurn, upstream = claudeTurn, provider
		}
		status, out := post(t, gw, request(model, foreignTurn, toolResult))
		sent := upstream.requests()
		if status != http.StatusOK || out != "accepted" || len(sent) != 2 {
			t.Fatalf("%s: status %d, response %q, requests %d", model, status, out, len(sent))
		}
		if strings.Contains(sent[1], `"thinking","signature"`) {
			t.Errorf("%s: the retry carries thinking blocks: %s", model, sent[1])
		}
	}
}

func TestUnrelatedErrorIsReturnedAsIs(t *testing.T) {
	gw, _, provider := setup(t)
	provider.reply = func(w http.ResponseWriter, body string) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, "error "+gjson.Get(body, "thinking.type").String())
	}
	// The retry is rejected too: the client sees the original error.
	if _, out := post(t, gw, request("qwen", qwenTurn, toolResult)); out != "error adaptive" {
		t.Errorf("response %q", out)
	}
	// Nothing to retry when the request is conservative already.
	before := len(provider.requests())
	post(t, gw, `{"model":"qwen","messages":[]}`)
	if got := len(provider.requests()) - before; got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
}

// A provider that refuses a history refuses it for the whole tool loop, so
// only the first request of the loop is sent twice.
func TestRejectionIsRememberedUntilTheToolLoopEnds(t *testing.T) {
	gw, _, provider := setup(t)
	rejectThinking(provider)
	steps := []struct {
		messages []string
		requests int // requests the provider has seen so far
	}{
		{[]string{claudeTurn, toolResult}, 2},                         // rejected, then retried
		{[]string{claudeTurn, toolResult, qwenTurn, toolResult}, 3},   // same loop: conservative at once
		{[]string{claudeTurn, toolResult, qwenTurn, userQuestion}, 5}, // next user message: full form again
	}
	for i, step := range steps {
		if status, _ := post(t, gw, request("qwen", step.messages...)); status != http.StatusOK {
			t.Fatalf("step %d: status %d", i, status)
		}
		if got := len(provider.requests()); got != step.requests {
			t.Fatalf("step %d: provider saw %d requests, want %d", i, got, step.requests)
		}
	}
}

// A model taken out of the configuration may still be on offer in a client
// that listed it earlier. Such a client is told what happened.
func TestRemovedModelIsReported(t *testing.T) {
	claude, provider := newFake(t), newFake(t)
	cfg := &config.Config{
		Providers: map[string]config.Provider{"local": {BaseURL: provider.URL}},
		Models:    []config.Model{{ID: "qwen", Provider: "local", Upstream: "qwen"}},
	}
	g := New(cfg)
	g.anthropic.url = baseURL(claude.URL)
	server := httptest.NewServer(&httputil.ReverseProxy{Rewrite: func(*httputil.ProxyRequest) {}, Transport: g})
	t.Cleanup(server.Close)

	g.Configure(&config.Config{})
	status, out := post(t, server.URL, `{"model":"qwen","messages":[]}`)
	if status != http.StatusNotFound || !strings.Contains(out, "removed from the configuration") {
		t.Errorf("status %d, response %s", status, out)
	}
	if len(claude.requests())+len(provider.requests()) != 0 {
		t.Error("the request was sent on")
	}

	// A model the gateway never served is none of its business.
	post(t, server.URL, `{"model":"claude-opus","messages":[]}`)
	if len(claude.requests()) != 1 {
		t.Error("an unknown model did not go to Anthropic")
	}

	// Put back, the model is served again.
	g.Configure(cfg)
	if status, _ := post(t, server.URL, `{"model":"qwen","messages":[]}`); status != http.StatusOK || len(provider.requests()) != 1 {
		t.Errorf("after the model came back: status %d, provider requests %d", status, len(provider.requests()))
	}
}
