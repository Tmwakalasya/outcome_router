package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/outcome-router/outcome-router/internal/config"
	"github.com/outcome-router/outcome-router/internal/domain"
)

type Client struct {
	httpClient *http.Client
	mu         sync.Mutex
	circuits   map[string]*circuit
}

type circuit struct {
	failures  int
	openUntil time.Time
}

type UpstreamError struct {
	StatusCode int
	Body       []byte
	ModelID    string
	ProviderID string
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("upstream %s/%s returned status %d", e.ProviderID, e.ModelID, e.StatusCode)
}

func NewClient(transport http.RoundTripper) *Client {
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &Client{
		httpClient: &http.Client{Transport: transport},
		circuits:   map[string]*circuit{},
	}
}

func (c *Client) Available(providerID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.circuits[providerID]
	if state == nil {
		return true
	}
	if !state.openUntil.IsZero() && time.Now().After(state.openUntil) {
		state.openUntil = time.Time{}
		state.failures = 0
	}
	return state.openUntil.IsZero()
}

func (c *Client) Do(
	ctx context.Context,
	providerConfig config.Provider,
	model domain.Model,
	body []byte,
	requestHeaders http.Header,
) (*http.Response, error) {
	if !c.Available(providerConfig.ID) {
		return nil, fmt.Errorf("provider %s circuit is open", providerConfig.ID)
	}
	timeout := time.Duration(providerConfig.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer func() {
		if requestContext.Err() != nil {
			cancel()
		}
	}()

	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, providerConfig.BaseURL, bytes.NewReader(body))
	if err != nil {
		cancel()
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", requestHeaders.Get("Accept"))
	if request.Header.Get("Accept") == "" {
		request.Header.Set("Accept", "application/json, text/event-stream")
	}
	if providerConfig.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+providerConfig.APIKey)
	}
	for key, value := range providerConfig.Headers {
		request.Header.Set(key, value)
	}
	for _, header := range []string{"OpenAI-Organization", "OpenAI-Project", "User-Agent"} {
		if value := requestHeaders.Get(header); value != "" {
			request.Header.Set(header, value)
		}
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		cancel()
		c.recordFailure(providerConfig)
		return nil, err
	}
	response.Body = &cancelOnClose{ReadCloser: response.Body, cancel: cancel}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		c.recordSuccess(providerConfig.ID)
		return response, nil
	}
	errorBody, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	_ = response.Body.Close()
	if retryableStatus(response.StatusCode) {
		c.recordFailure(providerConfig)
	}
	if readErr != nil {
		return nil, errors.Join(readErr, &UpstreamError{
			StatusCode: response.StatusCode, ModelID: model.ID, ProviderID: providerConfig.ID,
		})
	}
	return nil, &UpstreamError{
		StatusCode: response.StatusCode,
		Body:       errorBody,
		ModelID:    model.ID,
		ProviderID: providerConfig.ID,
	}
}

func retryableStatus(status int) bool {
	return status == http.StatusRequestTimeout ||
		status == http.StatusConflict ||
		status == http.StatusTooManyRequests ||
		status >= 500
}

func Retryable(err error) bool {
	var upstream *UpstreamError
	if errors.As(err, &upstream) {
		return retryableStatus(upstream.StatusCode)
	}
	return true
}

func (c *Client) recordFailure(providerConfig config.Provider) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.circuits[providerConfig.ID]
	if state == nil {
		state = &circuit{}
		c.circuits[providerConfig.ID] = state
	}
	state.failures++
	limit := providerConfig.CircuitFailures
	if limit <= 0 {
		limit = 3
	}
	if state.failures >= limit {
		duration := time.Duration(providerConfig.CircuitOpenMS) * time.Millisecond
		if duration <= 0 {
			duration = 30 * time.Second
		}
		state.openUntil = time.Now().Add(duration)
	}
}

func (c *Client) recordSuccess(providerID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.circuits, providerID)
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}
