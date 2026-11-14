// Package menu puts the user's models into the model menu of Claude Desktop.
//
// Desktop builds the menu from the model_selector_config its session host,
// claude.ai, returns at start-up, and saves the choice back to that host.
// The menu handler sits on the decrypted connection to claude.ai. It appends
// the configured models to the list on the way in and, since the host does
// not know those models, confirms their selection itself.
package menu

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
)

// Host is the session host of Claude Desktop.
const Host = "claude.ai"

// Model is a model to put into a list of models.
type Model struct {
	ID string
	// Name is what the list calls the model. A model without a name is
	// never added to a list; it only has its window set where it is listed.
	Name string
	// Context is the model's context window in tokens; zero is unknown.
	Context int
}

// Menu is an http.RoundTripper for the decrypted connection to Host.
type Menu struct {
	// Models returns the models to show in the menu.
	Models func() []Model
	// Refresh, if set, is called right before the menu is built for
	// Desktop. Bringing the gateway up to date there means it never offers
	// a model it cannot serve.
	Refresh func()
	// Transport reaches the real host.
	Transport http.RoundTripper
}

// PACPath is where the gateway serves the proxy auto-config script that
// Claude Desktop is pointed at.
const PACPath = "/proxy.pac"

// PACScript returns a proxy auto-config script that sends the connections
// to Host, and no others, through the proxy at address.
func PACScript(address string) string {
	return fmt.Sprintf("function FindProxyForURL(url, host) {\n"+
		"  return host == %q ? \"PROXY %s\" : \"DIRECT\";\n}\n", Host, address)
}

// PAC serves PACScript.
func PAC(address string) http.Handler {
	script := PACScript(address)
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ns-proxy-autoconfig")
		io.WriteString(w, script)
	})
}

var (
	bootstrapPath = regexp.MustCompile(`^/(edge-)?api/bootstrap(/[^/]+/app_start)?$`)
	selectionPath = regexp.MustCompile(`^/api/organizations/[^/]+/model_selector_state/[^/]+$`)
)

// codeSurfaces are the menus that belong to Claude Code sessions.
var codeSurfaces = map[string]bool{"cc": true, "ccd": true}

func (m *Menu) RoundTrip(r *http.Request) (*http.Response, error) {
	switch {
	case r.Method == http.MethodGet && bootstrapPath.MatchString(r.URL.Path):
		return m.bootstrap(r)
	case r.Method != http.MethodGet && selectionPath.MatchString(r.URL.Path):
		return m.selection(r)
	}
	return m.Transport.RoundTrip(r)
}

// bootstrap adds the models to the menus in the host's start-up response.
func (m *Menu) bootstrap(r *http.Request) (*http.Response, error) {
	if m.Refresh != nil {
		m.Refresh()
	}
	r.Header.Del("Accept-Encoding") // the response has to arrive uncompressed
	resp, err := m.Transport.RoundTrip(r)
	if err != nil || resp.StatusCode != http.StatusOK {
		return resp, err
	}
	return resp, AddModels(resp, m.Models())
}

// AddModels puts models into every Claude Code menu of a response that
// lists models, the way claude.ai and the Anthropic API both do.
func AddModels(resp *http.Response, models []Model) error {
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}
	if extended, menus := extend(body, models); menus > 0 {
		slog.Info("models put into a model list", "host", resp.Request.URL.Host, "path", resp.Request.URL.Path, "menus", menus)
		body = extended
	}
	setBody(resp, body)
	return nil
}

// extend puts the models into every Claude Code menu found in a JSON
// document and reports how many menus it found. A document it cannot parse
// is left alone.
func extend(document []byte, models []Model) (extended []byte, menus int) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var root any
	if decoder.Decode(&root) != nil {
		return document, 0
	}
	walk(root, func(object map[string]any) {
		surfaces, _ := object["model_selector_config"].([]any)
		for _, s := range surfaces {
			surface, _ := s.(map[string]any)
			rows, isList := surface["models"].([]any)
			if id, _ := surface["id"].(string); !codeSurfaces[id] || !isList {
				continue
			}
			surface["models"] = merge(rows, models)
			menus++
		}
	})
	if menus == 0 {
		return document, 0
	}
	extended, err := json.Marshal(root)
	if err != nil {
		return document, 0
	}
	return extended, menus
}

// merge returns rows with the models in them: a model that is listed gets
// its context window set, a named model that is not gets a row of its own.
func merge(rows []any, models []Model) []any {
	listed := map[string]map[string]any{}
	for _, r := range rows {
		row, _ := r.(map[string]any)
		id, _ := row["id"].(string)
		listed[id] = row
	}
	for _, model := range models {
		row, isListed := listed[model.ID]
		switch {
		case !isListed && model.Name == "":
			continue
		case !isListed:
			row = map[string]any{
				"id":           model.ID,
				"name":         model.Name,
				"short_name":   model.Name,
				"section":      "main",
				"capabilities": map[string]any{},
				"thinking":     map[string]any{"type": "none"},
				// Claude Code offers only the models listed for its provider.
				"offered_on": []string{"first_party"},
			}
			listed[model.ID] = row
			rows = append(rows, row)
		}
		if model.Context > 0 {
			runtime, _ := row["runtime"].(map[string]any)
			if runtime == nil {
				runtime = map[string]any{}
				row["runtime"] = runtime
			}
			runtime["max_input_tokens"] = model.Context
		}
	}
	return rows
}

// walk calls visit for every JSON object in value.
func walk(value any, visit func(map[string]any)) {
	switch v := value.(type) {
	case map[string]any:
		visit(v)
		for _, child := range v {
			walk(child, visit)
		}
	case []any:
		for _, child := range v {
			walk(child, visit)
		}
	}
}

// selection saves the user's choice of a model. The host refuses models it
// does not know; for those the handler confirms the choice itself, echoing
// the request as the saved state.
func (m *Menu) selection(r *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		return nil, err
	}
	var choice struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &choice) // an unreadable body names no model of ours
	if !slices.ContainsFunc(m.Models(), func(model Model) bool { return model.ID == choice.Model }) {
		r.Body = io.NopCloser(bytes.NewReader(body))
		return m.Transport.RoundTrip(r)
	}
	slog.Info("Desktop menu selection confirmed locally", "model", choice.Model)
	resp := &http.Response{
		Status:     "200 OK",
		StatusCode: http.StatusOK,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Request:    r,
	}
	setBody(resp, body)
	return resp, nil
}

func setBody(resp *http.Response, body []byte) {
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	resp.Header.Del("Etag") // the body no longer matches it
}
