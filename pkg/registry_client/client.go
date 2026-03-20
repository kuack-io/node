package registry_client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

var ErrRegistryStatus = errors.New("registry returned error status")

// WasmConfig describes how to execute a WASM image.
type WasmConfig struct {
	Type       string   `json:"type"`
	Path       string   `json:"path"`
	Variant    string   `json:"variant"`
	Env        []string `json:"env,omitempty"`
	Entrypoint []string `json:"entrypoint,omitempty"`
	Cmd        []string `json:"cmd,omitempty"`
}

// Client interacts with the external registry service.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

const defaultTimeout = 10 * time.Second

// NewClient creates a new registry client.
func NewClient(registryURL string) *Client {
	return &Client{
		baseURL: registryURL,
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
	}
}

// ResolveWasmConfig resolves the WASM config for an image.
func (c *Client) ResolveWasmConfig(ctx context.Context, imageRef string) (*WasmConfig, error) {
	endpoint := fmt.Sprintf("%s/resolve?image=%s", c.baseURL, url.QueryEscape(imageRef))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call registry resolve: %w", err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %d", ErrRegistryStatus, resp.StatusCode)
	}

	var config WasmConfig

	err = json.NewDecoder(resp.Body).Decode(&config)
	if err != nil {
		return nil, fmt.Errorf("failed to decode config: %w", err)
	}

	return &config, nil
}

// GetRegistryURL returns the base URL of the registry.
func (c *Client) GetRegistryURL() string {
	return c.baseURL
}
