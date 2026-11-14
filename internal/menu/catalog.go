package menu

import (
	"net/http"
	"regexp"
)

// Catalog is an http.RoundTripper for the Anthropic API that tells Claude
// Code the context windows of the user's models.
//
// A session asks the API for the models it may use, and a row of the answer
// carries the model's context window. Catalog puts its models into that
// answer on the way to the session.
type Catalog struct {
	// Models returns the models whose context window is known.
	Models func() []Model
	// Next reaches the API.
	Next http.RoundTripper
}

var catalogPath = regexp.MustCompile(`^/api/(organizations/[^/]+/)?model_selector/[^/]+$`)

func (c *Catalog) RoundTrip(r *http.Request) (*http.Response, error) {
	models := c.Models()
	if r.Method != http.MethodGet || !catalogPath.MatchString(r.URL.Path) || len(models) == 0 {
		return c.Next.RoundTrip(r)
	}
	// The whole answer is needed every time, uncompressed: "not modified"
	// would leave the session with the windows of an older configuration.
	r.Header.Del("Accept-Encoding")
	r.Header.Del("If-None-Match")
	resp, err := c.Next.RoundTrip(r)
	if err != nil || resp.StatusCode != http.StatusOK {
		return resp, err
	}
	return resp, AddModels(resp, models)
}
