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
	"sync/atomic"

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
	// extra holds the fields a third-party provider is sent on top of the
	// client's request.
	extra map[string]any
}

// Gateway sends a Claude Code request to the provider of its model. It is an
// http.RoundTripper, so it does not care whether the request was addressed to
// the gateway or intercepted on its way to Anthropic.
type Gateway struct {
	anthropic *upstream
	// models holds the upstream of every model ID the gateway answers to.
	// Configure replaces it as a whole while requests are being served.
	models atomic.Pointer[map[string]*upstream]
	// retired holds the model IDs the gateway used to answer to. A client
	// that still names one is told so, instead of being sent to Anthropic
	// with a model Anthropic has never heard of.
	retired   sync.Map
	transport http.RoundTripper
}

// New builds a gateway from a loaded configuration.
func New(cfg *config.Config) *Gateway {
	g := &Gateway{
		anthropic: &upstream{name: "anthropic", url: baseURL("https://" + AnthropicHost)},
		transport: DirectTransport(),
	}
	g.Configure(cfg)
	return g
}

// Configure makes the gateway serve the models of cfg. It may be called
// while the gateway is in use: requests in flight finish on the models they
// started with.
func (g *Gateway) Configure(cfg *config.Config) {
	models := map[string]*upstream{}
	for id, m := range cfg.Routes() {
		p := cfg.Providers[m.Provider]
		models[id] = &upstream{
			name:       m.Provider,
			url:        baseURL(p.BaseURL),
			thirdParty: true,
			key:        p.APIKey,
			model:      m.Upstream,
			extra:      m.ExtraBody,
		}
	}
	if old := g.models.Swap(&models); old != nil {
		for id := range *old {
			if _, kept := models[id]; !kept {
				g.retired.Store(id, struct{}{})
			}
		}
	}
	for id := range models {
		g.retired.Delete(id)
	}
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
	up := g.anthropic
	if u, ok := (*g.models.Load())[model]; ok {
		up = u
	} else if _, gone := g.retired.Load(model); gone {
		slog.Warn("request for a model that is no longer configured", "model", model)
		return removed(r, model), nil
	}
	if up.thirdParty {
		if continuesThread(body) {
			slog.Info("asked for the whole history", "model", model)
			return stateless(r), nil
		}
		body = portable(body, up.model, up.extra)
	}
	body = unmark(body, up.thirdParty)

	out := address(r, up)
	out.ContentLength = int64(len(body))
	// The transport sends the body again if the connection it picked turns
	// out to be closed by the upstream.
	out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	out.Body, _ = out.GetBody()
	resp, err := g.transport.RoundTrip(out)
	if err != nil {
		slog.Error("upstream did not answer", "model", model, "upstream", up.name, "error", err)
		return nil, err
	}
	slog.Info("request", "method", r.Method, "path", r.URL.Path,
		"model", model, "upstream", up.name, "status", resp.StatusCode)
	if up.thirdParty && resp.StatusCode == http.StatusOK {
		if err := markResponse(resp, gjson.GetBytes(body, "stream").Bool()); err != nil {
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

// markResponse marks the thinking blocks in a third-party provider's
// response. The client reads the response as the request asked for it, as a
// stream or as a single message, whatever content type the provider declares.
func markResponse(resp *http.Response, stream bool) error {
	if stream {
		resp.Body = newMarkStream(resp.Body)
		resp.ContentLength = -1 // the marks change the length
		resp.Header.Del("Content-Length")
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}
	body = markMessage(body)
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return nil
}
