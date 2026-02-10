package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// errNotFound is returned by getJSON/postJSON for 404 responses.
// Callers can detect it with errors.Is(err, errNotFound).
var errNotFound = errors.New("not found")

// readErrorBody reads a JSON error response body and returns the "error" field,
// falling back to the raw body text or HTTP status.
func readErrorBody(body io.Reader, status string) string {
	data, err := io.ReadAll(io.LimitReader(body, 1024))
	if err != nil || len(data) == 0 {
		return status
	}
	var errResp struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &errResp) == nil && errResp.Error != "" {
		return errResp.Error
	}
	if s := strings.TrimSpace(string(data)); s != "" {
		return s
	}
	return status
}

// getJSON performs a GET request and decodes the JSON response into out.
// Returns errNotFound for 404 responses. Other errors include the server's message.
func (m tuiModel) getJSON(path string, out any) error {
	url := m.serverAddr + path
	resp, err := m.client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %s", errNotFound, readErrorBody(resp.Body, resp.Status))
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", readErrorBody(resp.Body, resp.Status))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// postJSON performs a POST request with a JSON body and decodes the response into out.
// If out is nil, the response body is discarded.
// Returns errNotFound (wrapped with server message) for 404 responses.
func (m tuiModel) postJSON(path string, in any, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	resp, err := m.client.Post(m.serverAddr+path, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %s", errNotFound, readErrorBody(resp.Body, resp.Status))
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("%s", readErrorBody(resp.Body, resp.Status))
	}

	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	} else {
		io.Copy(io.Discard, resp.Body)
	}
	return nil
}
