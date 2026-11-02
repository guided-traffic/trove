package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// client calls the REST API v4 with a personal access token. Errors name the method, the path and
// the status, never the token.
type client struct {
	base  string
	token string
	hc    *http.Client
}

type apiError struct {
	method, path string
	status       int
	body         string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s %s: HTTP %d %s", e.method, e.path, e.status, e.body)
}

func isStatus(err error, status int) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.status == status
}

func isNotFound(err error) bool { return isStatus(err, http.StatusNotFound) }

const maxBody = 64 << 20

// call sends in as JSON (when not nil) and decodes the answer into out (when not nil).
func (c *client) call(ctx context.Context, method, path string, in, out any) error {
	data, err := c.send(ctx, method, path, in)
	if err != nil || out == nil || len(data) == 0 {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	return nil
}

// raw returns the body of a GET as bytes (job logs, artifact files).
func (c *client) raw(ctx context.Context, path string) ([]byte, error) {
	return c.send(ctx, http.MethodGet, path, nil)
}

func (c *client) send(ctx context.Context, method, path string, in any) ([]byte, error) {
	var body io.Reader = http.NoBody
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/api/v4"+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	if resp.StatusCode >= 300 {
		return nil, &apiError{method: method, path: path, status: resp.StatusCode, body: snippet(data)}
	}
	return data, nil
}

// snippet is the start of an error body, on one line.
func snippet(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}

// answer is what get read of a response: its status, its header and its body.
type answer struct {
	status int
	header http.Header
	body   []byte
}

// get fetches an absolute URL of the instance without the token: the discovery document and the
// key set are public.
func (c *client) get(ctx context.Context, url string, header http.Header) (answer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return answer{}, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return answer{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return answer{status: resp.StatusCode, header: resp.Header, body: data}, err
}
