package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// The only two API endpoints stele ever sends a token to (D65). They are
// fixed: nothing in "repo" changes where a token goes. Tests replace them.
var (
	githubAPI = "https://api.github.com"
	gitlabAPI = "https://gitlab.com/api/v4"
)

// httpClient is the client for both APIs. It never follows a redirect, so a
// token is never sent on to another place. Tests replace it.
var httpClient = &http.Client{Timeout: 30 * time.Second, CheckRedirect: noRedirect}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// maxResponse caps what stele reads of an API response.
const maxResponse = 1 << 20

// errRejected is a token the host refused: stele tries the next login.
var errRejected = errors.New("rejected the login")

// mrRequest is the merge request stele asks for.
type mrRequest struct {
	Base   string // the default branch
	Branch string // the publish branch
	Title  string
	Body   string
	Pushed bool // false: the branch already held this stele.md, so a closed request may be reopened
}

// mrResult is the merge request the host has.
type mrResult struct {
	Link   string
	Action string // "opened", "found" or "reopened"
}

// apiError is a host's API answering with an error.
type apiError struct {
	host   string
	status int
	text   string
}

func (e *apiError) Error() string { return fmt.Sprintf("%s API: %s", e.host, e.text) }

// call sends one API request with token and decodes a JSON answer into out.
// 401 and 403 are errRejected. Neither the token nor the response body ever
// goes into an error.
func call(ctx context.Context, host, method, url, token string, extra map[string]string, body, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "stele")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s API: %w", host, redact(err))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("%s API: %w", host, err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return resp.StatusCode, errRejected
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return resp.StatusCode, &apiError{host: host, status: resp.StatusCode, text: resp.Status}
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s API: unreadable answer: %w", host, err)
		}
	}
	return resp.StatusCode, nil
}

// redact keeps a transport error's kind but not its request, whose URL is
// stele's own and whose headers are never printed by net/http anyway.
func redact(err error) error {
	var ue interface{ Unwrap() error }
	if errors.As(err, &ue) && ue.Unwrap() != nil {
		return ue.Unwrap()
	}
	return err
}
