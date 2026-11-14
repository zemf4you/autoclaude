// Package gateway routes Anthropic Messages API requests by their model field
// and keeps every request valid for its provider whatever the history holds.
package gateway

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"

	"github.com/tidwall/gjson"

	"github.com/zemf4you/autoclaude/internal/config"
)

// AnthropicHost is where requests for models that are not configured go.
const AnthropicHost = "api.anthropic.com"

// upstream is where requests for a model are sent.
type upstream struct {
	name string
	url  *url.URL
	// thirdParty tells providers from Anthropic, which receives requests
	// with the client's own credentials.
	thirdParty bool
	key        string
	// model is the model's name at a third-party provider.
	model string
}

// Gateway sends a Claude Code request to the provider of its model. It is an
// http.RoundTripper, so it does not care whether the request was addressed to
// the gateway or intercepted on its way to Anthropic.
type Gateway struct {
	anthropic *upstream
	models    map[string]*upstream
	transport http.RoundTripper
	// rejected holds the tool loops, keyed by session and model, whose
	// history the provider has refused to take with thinking blocks.
	rejected sync.Map
}

// New builds a gateway from a loaded configuration.
func New(cfg *config.Config) *Gateway {
	g := &Gateway{
		anthropic: &upstream{name: "anthropic", url: baseURL("https://" + AnthropicHost)},
		models:    map[string]*upstream{},
		transport: DirectTransport(),
	}
	for model, route := range cfg.Models {
		p := cfg.Providers[route.Provider]
		g.models[model] = &upstream{
			name:       route.Provider,
			url:        baseURL(p.BaseURL),
			thirdParty: true,
			key:        p.APIKey,
			model:      route.Model,
		}
	}
	return g
}

// DirectTransport returns a transport that ignores the proxy environment.
// Claude Code is pointed at the gateway with HTTPS_PROXY; a gateway started
// from the same environment would otherwise send its requests to itself.
func DirectTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	return t
}

// baseURL parses an address already validated by the configuration.
func baseURL(raw string) *url.URL {
	u, _ := url.Parse(raw)
	if u.Path == "" {
		u.Path = "/" // JoinPath would otherwise return a relative path
	}
	return u
}

// RoundTrip picks the provider by the model field and returns its response.
func (g *Gateway) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		defer r.Body.Close()
		var err error
		if body, err = io.ReadAll(r.Body); err != nil {
			return nil, err
		}
	}
	model := gjson.GetBytes(body, "model").String()
	up, owner := g.anthropic, "" // unmarked thinking blocks belong to Claude
	if u, ok := g.models[model]; ok {
		up, owner = u, model
	}
	if up.thirdParty {
		if continuesThread(body) {
			slog.Info("asked for the whole history", "model", model)
			return stateless(r), nil
		}
		body = portable(body, up.model)
	}
	body, mixed := native(body, owner)

	x := &exchange{
		request: address(r, up),
		body:    body,
		loop:    r.Header.Get("X-Claude-Code-Session-Id") + " " + model,
		// A conversation that never left Claude is none of the gateway's
		// business: its errors go back to the client as they are.
		retriable: up.thirdParty || mixed,
	}
	resp, err := g.send(x)
	if err != nil {
		return nil, err
	}
	slog.Info("request", "method", r.Method, "path", r.URL.Path,
		"model", model, "upstream", up.name, "status", resp.StatusCode)
	if up.thirdParty && resp.StatusCode == http.StatusOK {
		if err := markResponse(resp, model, gjson.GetBytes(body, "stream").Bool()); err != nil {
			resp.Body.Close()
			return nil, err
		}
	}
	return resp, nil
}

// address returns a copy of the request addressed to the upstream.
func address(r *http.Request, up *upstream) *http.Request {
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Host = ""
	out.URL = up.url.JoinPath(r.URL.EscapedPath())
	out.URL.RawQuery = r.URL.RawQuery
	if !up.thirdParty {
		return out
	}
	// A third-party provider gets only the headers of the public Messages
	// API. Subscription credentials and the identifiers Claude Code sends
	// along must never leave Anthropic. Accept-Encoding is left out as
	// well: the response is edited on the fly and has to stay uncompressed.
	out.Header = http.Header{}
	for _, name := range []string{"Accept", "Content-Type", "Anthropic-Version", "Anthropic-Beta"} {
		if values := r.Header.Values(name); len(values) > 0 {
			out.Header[name] = values
		}
	}
	if up.key != "" {
		out.Header.Set("Authorization", "Bearer "+up.key)
	}
	return out
}

// exchange is a request on its way to an upstream.
type exchange struct {
	request *http.Request
	body    []byte
	// loop identifies the tool loop the request belongs to.
	loop string
	// retriable allows the conservative retry.
	retriable bool
}

// send performs the request with a safety net: if the provider rejects the
// history, the request is retried once in its conservative form, without
// thinking. A 400 arrives before the stream starts, so the client never
// notices the retry.
//
// A history rejected once stays rejected until the tool loop ends, because
// the loop only appends to it. The rest of the loop therefore goes out in
// the conservative form right away, and the full form is tried again on the
// next user message: one refusal costs one extra request, not one per step.
func (g *Gateway) send(x *exchange) (*http.Response, error) {
	body := x.body
	safe, differs := withoutThinking(body)
	canRetry := x.retriable && differs
	if !inToolLoop(body) {
		g.rejected.Delete(x.loop)
	} else if _, rejected := g.rejected.Load(x.loop); rejected && canRetry {
		body, canRetry = safe, false
	}

	setBody(x.request, body)
	resp, err := g.transport.RoundTrip(x.request)
	if err != nil || resp.StatusCode != http.StatusBadRequest || !canRetry {
		return resp, err
	}
	retry := x.request.Clone(x.request.Context())
	setBody(retry, safe)
	second, err := g.transport.RoundTrip(retry)
	if err != nil {
		return resp, nil
	}
	if second.StatusCode >= http.StatusBadRequest {
		second.Body.Close()
		return resp, nil // thinking was not the cause: the client gets the original error
	}
	resp.Body.Close()
	g.rejected.Store(x.loop, struct{}{})
	slog.Warn("history rejected, request retried without thinking", "upstream", x.request.URL.Host)
	return second, nil
}

// inToolLoop reports whether the request continues a tool loop, that is,
// whether its last message returns tool results.
func inToolLoop(body []byte) bool {
	return gjson.GetBytes(body, `messages.@reverse.0.content.#(type=="tool_result")`).Exists()
}

// markResponse marks the thinking blocks in a third-party provider's
// response. The client reads the response as the request asked for it, as a
// stream or as a single message, whatever content type the provider declares.
func markResponse(resp *http.Response, model string, stream bool) error {
	if stream {
		resp.Body = newMarkStream(resp.Body, model)
		resp.ContentLength = -1 // the marks change the length
		resp.Header.Del("Content-Length")
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}
	body = markMessage(body, model)
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return nil
}

func setBody(r *http.Request, body []byte) {
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
}
