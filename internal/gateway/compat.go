package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// billingHeader starts the system block in which Claude Code reports usage
// to Anthropic. The block carries a hash that differs in every request.
const billingHeader = "x-anthropic-billing-header:"

// portable rewrites a request into the form third-party providers accept:
//
//   - The model is addressed by the name the provider knows it under.
//   - Claude Code puts messages with the system role in the middle of the
//     history, which the Anthropic API allows. The chat templates of many
//     models allow a system message only first and reject the request
//     otherwise. Such messages become user messages and keep their position.
//   - The billing block opens the system prompt and changes with every
//     request, which would defeat the provider's prefix cache: a local model
//     would read the whole prompt again on every turn. It is removed.
//   - The fields that drive Anthropic's server-side history mean nothing to
//     another provider, and metadata carries account and device identifiers.
//     They are removed too.
func portable(body []byte, model string) []byte {
	body, _ = sjson.SetBytes(body, "model", model)
	for i, message := range gjson.GetBytes(body, "messages").Array() {
		if message.Get("role").String() == "system" {
			body, _ = sjson.SetBytes(body, fmt.Sprintf("messages.%d.role", i), "user")
		}
	}
	system := gjson.GetBytes(body, "system").Array()
	for i := len(system) - 1; i >= 0; i-- { // backwards: a removal shifts the blocks after it
		if strings.HasPrefix(system[i].Get("text").String(), billingHeader) {
			body, _ = sjson.DeleteBytes(body, fmt.Sprintf("system.%d", i))
		}
	}
	for _, field := range []string{"thread", "diagnostics", "metadata"} {
		body, _ = sjson.DeleteBytes(body, field)
	}
	return body
}

// continuesThread reports whether the request carries only the new messages
// and relies on Anthropic's servers to hold the rest of the history.
func continuesThread(body []byte) bool {
	return gjson.GetBytes(body, "thread.type").String() == "continue"
}

// stateless refuses to continue a server-side history. Claude Code responds
// by resending the request with the whole history, and keeps sending it
// whole for the rest of the session (observed with Claude Code 2.1.321: it
// treats any 400 that mentions "thread:" as the server declining threads).
func stateless(r *http.Request) *http.Response {
	return failure(r, http.StatusBadRequest, "invalid_request_error",
		"thread: this model keeps no conversation state, send the whole history")
}

// removed tells the client that the model it asked for has been taken out of
// the configuration since it was offered.
func removed(r *http.Request, model string) *http.Response {
	return failure(r, http.StatusNotFound, "not_found_error",
		fmt.Sprintf("autoclaude: the model %q has been removed from the configuration; pick another model", model))
}

// failure builds an error response in the format of the Messages API.
func failure(r *http.Request, status int, kind, message string) *http.Response {
	body, _ := json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": kind, "message": message},
	})
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode:    status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": {"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       r,
	}
}
