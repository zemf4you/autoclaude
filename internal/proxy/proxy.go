// Package proxy accepts Claude traffic and hands it to its handlers.
//
// One address serves both ways of connecting:
//   - the CLI talks to it directly, as to the API (ANTHROPIC_BASE_URL);
//   - Desktop does not let the API address be changed, so it uses the same
//     address as an HTTPS proxy (HTTPS_PROXY). Connections to intercepted
//     hosts are decrypted with the proxy's own certificates; all others are
//     tunnelled untouched.
package proxy

import (
	"crypto/tls"
	"log/slog"
	"net/http"
	"net/http/httputil"

	"github.com/elazarl/goproxy"

	"github.com/zemf4you/autoclaude/internal/gateway"
)

// Interception names a host whose connections the proxy decrypts.
type Interception struct {
	Host string
	// CA issues the certificates the proxy presents as Host.
	CA tls.Certificate
	// Handler receives the decrypted requests.
	Handler http.RoundTripper
}

// New returns a handler that passes direct requests to direct and decrypts
// the connections named by interceptions.
func New(direct http.Handler, interceptions ...Interception) http.Handler {
	p := goproxy.NewProxyHttpServer()
	p.Logger = slog.NewLogLogger(slog.Default().Handler(), slog.LevelWarn)
	// Like the gateway, the proxy must not obey a proxy environment that
	// points back at itself.
	p.Tr = gateway.DirectTransport()
	p.ConnectDial = nil
	p.NonproxyHandler = direct
	for _, i := range interceptions {
		decrypt := &goproxy.ConnectAction{Action: goproxy.ConnectMitm, TLSConfig: goproxy.TLSConfigFromCA(&i.CA)}
		p.OnRequest(goproxy.ReqHostIs(i.Host + ":443")).HandleConnectFunc(
			func(host string, _ *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) { return decrypt, host })
		p.OnRequest(goproxy.DstHostIs(i.Host)).DoFunc(
			func(r *http.Request, _ *goproxy.ProxyCtx) (*http.Request, *http.Response) {
				resp, err := i.Handler.RoundTrip(r)
				if err != nil {
					return r, goproxy.NewResponse(r, goproxy.ContentTypeText, http.StatusBadGateway, err.Error())
				}
				return r, resp
			})
	}
	return p
}

// API returns the handler for requests the CLI sends straight to the
// gateway. It rejects requests made by web pages: Claude Code never sends an
// Origin header, while a page open in the user's browser would, and could
// otherwise spend the provider keys the gateway adds to requests.
func API(gw http.RoundTripper) http.Handler {
	forward := &httputil.ReverseProxy{
		Rewrite:       func(*httputil.ProxyRequest) {}, // the gateway picks the destination
		Transport:     gw,
		FlushInterval: -1, // responses are streams and must not be buffered
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			http.Error(w, "requests from web pages are not accepted", http.StatusForbidden)
			return
		}
		forward.ServeHTTP(w, r)
	})
}
