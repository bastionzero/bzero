package httpclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"time"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	"github.com/cenkalti/backoff"
)

const (
	HTTPTimeout = time.Second * 30
)

type RequestMethod string

const (
	Get   RequestMethod = "GET"
	Post  RequestMethod = "POST"
	Patch RequestMethod = "PATCH"
)

type HTTPOptions struct {
	Endpoint string
	Body     []byte
	Headers  map[string][]string
	Params   map[string][]string
}

type HttpClient struct {
	logger *logger.Logger

	backoffParams *backoff.ExponentialBackOff

	targetUrl string
	body      io.Reader
	headers   map[string][]string
	params    map[string][]string
}

func New(
	logger *logger.Logger,
	serviceUrl string,
	options HTTPOptions,
) (*HttpClient, error) {

	if options.Endpoint != "" {
		combo, err := url.Parse(serviceUrl)
		if err != nil {
			return nil, err
		}
		combo.Path = path.Join(combo.Path, options.Endpoint)
		serviceUrl = combo.String()
	}

	if options.Headers == nil {
		options.Headers = make(map[string][]string)
	}

	if options.Params == nil {
		options.Params = make(map[string][]string)
	}

	return &HttpClient{
		logger:    logger,
		targetUrl: serviceUrl,
		body:      bytes.NewBuffer(options.Body),
		headers:   options.Headers,
		params:    options.Params,
	}, nil
}

func NewWithBackoff(
	logger *logger.Logger,
	serviceUrl string,
	options HTTPOptions,
) (*HttpClient, error) {
	backoffParams := backoff.NewExponentialBackOff()

	// Ref: https://github.com/cenkalti/backoff/blob/a78d3804c2c84f0a3178648138442c9b07665bda/exponential.go#L76
	// DefaultInitialInterval     = 500 * time.Millisecond
	// DefaultRandomizationFactor = 0.5
	// DefaultMultiplier          = 1.5
	// DefaultMaxInterval         = 60 * time.Second
	// DefaultMaxElapsedTime      = 15 * time.Minute

	backoffParams.MaxInterval = 15 * time.Minute
	backoffParams.MaxElapsedTime = 72 * time.Hour

	return New(logger, serviceUrl, options)
}

func (h *HttpClient) Post(ctx context.Context) (*http.Response, error) {
	return h.request(Post, ctx)
}

func (h *HttpClient) Patch(ctx context.Context) (*http.Response, error) {
	return h.request(Patch, ctx)
}

func (h *HttpClient) Get(ctx context.Context) (*http.Response, error) {
	return h.request(Get, ctx)
}

func (h *HttpClient) request(method RequestMethod, ctx context.Context) (*http.Response, error) {
	// If there is no backoff, then only execute request once
	if h.backoffParams == nil {
		return h.makeRequestOnce(method, ctx)
	}

	// Keep looping through our ticker, waiting for it to tell us when to retry
	ticker := backoff.NewTicker(h.backoffParams)
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("context cancelled before successful http response")
		case _, ok := <-ticker.C:
			if !ok {
				return nil, fmt.Errorf("failed to get successful http response after %s", h.backoffParams.MaxElapsedTime)
			}

			if response, err := h.makeRequestOnce(method, ctx); err != nil {
				nextRequestTime := h.backoffParams.NextBackOff().Round(time.Second)
				h.logger.Errorf("retrying in %s: %s", nextRequestTime, err)
			} else {
				return response, err
			}
		}
	}
}

func (h *HttpClient) makeRequestOnce(method RequestMethod, ctx context.Context) (*http.Response, error) {
	// Make our Client
	client := http.Client{
		Timeout: HTTPTimeout,
	}

	// Build our Request
	request, _ := http.NewRequestWithContext(ctx, string(method), h.targetUrl, h.body)
	request.Header = http.Header(h.headers)

	// Add params to request URL
	query := url.Values(h.params)
	request.URL.RawQuery = query.Encode()

	// Make our Request
	response, err := client.Do(request)
	if err != nil {
		return response, fmt.Errorf("%s request failed: %w", string(method), err)
	}

	// Check if request was successful
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response, fmt.Errorf("%s request failed with status code: %d", string(method), response.StatusCode)
	}

	return response, err
}
