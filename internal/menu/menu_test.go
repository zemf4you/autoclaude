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
				`{"id":"cc","models":[{"id":"claude-opus-5-5","name":"Opus 5.5","runtime":{"max_input_tokens":1000000,"family":"opus"}},{"id":"taken","name":"Taken"}]},`+
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
	models := []Model{{ID: "qwen", Name: "Qwen", Context: 131072}, {ID: "taken", Name: "Ignored"}}
	m := &Menu{Models: func() []Model { return models }, Transport: http.DefaultTransport}
	// The configuration is refreshed right before the menu is built from it.
	m.Refresh = func() { models = append(models, Model{ID: "fresh", Name: "Fresh"}) }
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

	if got := gjson.Get(out, `model_selector_config.#(id=="cc").models.#.id`).String(); got != `["claude-opus-5-5","taken","qwen","fresh"]` {
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

// A session learns the windows from the list the Anthropic API gives it.
func TestCatalogCarriesContextWindows(t *testing.T) {
	models := []Model{{ID: "qwen", Name: "Qwen", Context: 131072}, {ID: "claude-opus-5-5", Context: 200000}, {ID: "claude-gone", Context: 200000}}
	catalog := &Catalog{Models: func() []Model { return models }, Next: http.DefaultTransport}
	r, _ := http.NewRequest(http.MethodGet, host(t).URL+"/api/organizations/org/model_selector/ccd", nil)
	r.Header.Set("If-None-Match", `"cached"`)
	resp, err := catalog.RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	out := string(body)

	if got := gjson.Get(out, `model_selector_config.#(id=="cc").models.#.id`).String(); got != `["claude-opus-5-5","taken","qwen"]` {
		t.Errorf("a model without a name must not get a row: %s", got)
	}
	if got := gjson.Get(out, `model_selector_config.#(id=="cc").models.#(id=="qwen").runtime.max_input_tokens`).Int(); got != 131072 {
		t.Errorf("window of a model of its own = %d", got)
	}
	opus := gjson.Get(out, `model_selector_config.#(id=="cc").models.#(id=="claude-opus-5-5").runtime`)
	if opus.Get("max_input_tokens").Int() != 200000 || opus.Get("family").String() != "opus" {
		t.Errorf("a listed model keeps its row and gets the window: %s", opus)
	}
	if r.Header.Get("If-None-Match") != "" {
		t.Error("the list must be fetched whole every time")
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
