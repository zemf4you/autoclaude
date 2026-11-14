package menu

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// host is a stand-in for claude.ai: it serves a start-up response with two
// menus and refuses to save the choice of any model.
func host(t *testing.T) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			io.WriteString(w, `{"account":{"id":12345678901234567890},"model_selector_config":[`+
				`{"id":"cc","models":[{"id":"claude-opus-5-5","name":"Opus 5.5"},{"id":"taken","name":"Taken"}]},`+
				`{"id":"chat","models":[{"id":"claude-opus-5-5"}]}]}`)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"type":"model_not_selectable"}}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func do(t *testing.T, method, url, body string) (int, string) {
	names := map[string]string{"qwen": "Qwen", "taken": "Ignored"}
	m := &Menu{Models: func() map[string]string { return names }, Transport: http.DefaultTransport}
	// The configuration is refreshed right before the menu is built from it.
	m.Refresh = func() { names["fresh"] = "Fresh" }
	r, _ := http.NewRequest(method, url, strings.NewReader(body))
	resp, err := m.RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

func TestModelsAreAddedToCodeMenus(t *testing.T) {
	_, out := do(t, http.MethodGet, host(t).URL+"/api/bootstrap/org/app_start", "")

	if got := gjson.Get(out, `model_selector_config.#(id=="cc").models.#.id`).String(); got != `["claude-opus-5-5","taken","fresh","qwen"]` {
		t.Errorf("code menu = %s", got)
	}
	if got := gjson.Get(out, `model_selector_config.#(id=="cc").models.#(id=="qwen").name`).String(); got != "Qwen" {
		t.Errorf("name = %q", got)
	}
	if got := gjson.Get(out, `model_selector_config.#(id=="chat").models.#`).Int(); got != 1 {
		t.Errorf("chat menu has %d models, want 1", got)
	}
	if got := gjson.Get(out, "account.id").Raw; got != "12345678901234567890" {
		t.Errorf("a number lost precision: %s", got)
	}
}

func TestOtherResponsesPassUntouched(t *testing.T) {
	_, out := do(t, http.MethodGet, host(t).URL+"/api/organizations/org/chats", "")
	if !strings.Contains(out, `{"id":"taken","name":"Taken"}]}`) {
		t.Errorf("response changed: %s", out)
	}
}

func TestSelection(t *testing.T) {
	url := host(t).URL + "/api/organizations/org/model_selector_state/cc"

	// The host does not know our model: the choice is confirmed locally.
	if status, out := do(t, http.MethodPatch, url, `{"model":"qwen"}`); status != http.StatusOK || out != `{"model":"qwen"}` {
		t.Errorf("our model: status %d, response %s", status, out)
	}
	// The choice of a Claude model is the host's business.
	if status, _ := do(t, http.MethodPatch, url, `{"model":"claude-opus-5-5"}`); status != http.StatusBadRequest {
		t.Errorf("Claude model: status %d, want the host's answer", status)
	}
}

func TestPAC(t *testing.T) {
	w := httptest.NewRecorder()
	PAC("127.0.0.1:8484").ServeHTTP(w, httptest.NewRequest(http.MethodGet, PACPath, nil))
	want := "function FindProxyForURL(url, host) {\n  return host == \"claude.ai\" ? \"PROXY 127.0.0.1:8484\" : \"DIRECT\";\n}\n"
	if got := w.Body.String(); got != want {
		t.Errorf("script =\n%s", got)
	}
}
