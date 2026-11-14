package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/zemf4you/autoclaude/internal/config"
	"github.com/zemf4you/autoclaude/internal/gateway"
)

// setup starts the proxy in front of a gateway whose only model, "qwen",
// is served by provider.
func setup(t *testing.T, provider http.HandlerFunc) (proxyURL *url.URL, ca tls.Certificate) {
	upstream := httptest.NewServer(provider)
	t.Cleanup(upstream.Close)
	ca, _, err := LoadCA(t.TempDir(), "ca", gateway.AnthropicHost)
	if err != nil {
		t.Fatal(err)
	}
	gw := gateway.New(&config.Config{
		Providers: map[string]config.Provider{"local": {BaseURL: upstream.URL}},
		Models:    map[string]config.Route{"qwen": {Provider: "local", Model: "qwen"}},
	})
	server := httptest.NewServer(New(API(gw), Interception{Host: gateway.AnthropicHost, CA: ca, Handler: gw}))
	t.Cleanup(server.Close)
	proxyURL, _ = url.Parse(server.URL)
	return proxyURL, ca
}

const request = `{"model":"qwen","messages":[]}`

func echo(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	io.WriteString(w, r.URL.Path+" "+string(body))
}

func TestDirectRequest(t *testing.T) {
	proxyURL, _ := setup(t, echo)
	resp, err := http.Post(proxyURL.String()+"/v1/messages", "application/json", strings.NewReader(request))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out, _ := io.ReadAll(resp.Body); string(out) != "/v1/messages "+request {
		t.Errorf("provider saw %q", out)
	}
}

func TestWebPageRequestIsRefused(t *testing.T) {
	proxyURL, _ := setup(t, echo)
	req, _ := http.NewRequest(http.MethodPost, proxyURL.String()+"/v1/messages", strings.NewReader(request))
	req.Header.Set("Origin", "https://example.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestInterceptedRequest(t *testing.T) {
	proxyURL, ca := setup(t, echo)
	roots := x509.NewCertPool()
	roots.AddCert(ca.Leaf)
	client := &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{RootCAs: roots},
	}}
	resp, err := client.Post("https://api.anthropic.com/v1/messages", "application/json", strings.NewReader(request))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out, _ := io.ReadAll(resp.Body); string(out) != "/v1/messages "+request {
		t.Errorf("provider saw %q", out)
	}
}

// A streamed response must reach an intercepted client event by event.
func TestInterceptedStreamIsNotBuffered(t *testing.T) {
	release := make(chan struct{})
	proxyURL, ca := setup(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"ping\"}\n\n")
		w.(http.Flusher).Flush()
		<-release // the stream stays open until the client has read the first event
	})
	defer close(release)
	roots := x509.NewCertPool()
	roots.AddCert(ca.Leaf)
	client := &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{RootCAs: roots},
	}}
	resp, err := client.Post("https://api.anthropic.com/v1/messages", "application/json", strings.NewReader(`{"model":"qwen","stream":true,"messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	first := make([]byte, len("data: {\"type\":\"ping\"}\n\n"))
	if _, err := io.ReadFull(resp.Body, first); err != nil {
		t.Fatalf("first event did not arrive while the stream was open: %v", err)
	}
}
