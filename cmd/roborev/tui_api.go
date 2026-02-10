package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// getJSON performs a GET request and decodes the JSON response into out.
// Returns an error for non-200 responses, using the provided context for messages.
func (m tuiModel) getJSON(path string, out any) error {
	url := m.serverAddr + path
	resp, err := m.client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("not found")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", resp.Status)
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// postJSON performs a POST request with a JSON body and decodes the response into out.
// If out is nil, the response body is discarded.
// Returns an error for non-200/201 responses.
func (m tuiModel) postJSON(path string, in any, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	resp, err := m.client.Post(m.serverAddr+path, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("not found")
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("%s", resp.Status)
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
