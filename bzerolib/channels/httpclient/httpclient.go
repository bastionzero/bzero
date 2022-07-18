package httpclient

import (
	"bytes"
	"errors"
	"net/http"
	"time"

	"github.com/cenkalti/backoff"
)

const (
	httpTimeout = time.Second * 30
)

type RequestMethod string

const (
	Get   RequestMethod = "GET"
	Post  RequestMethod = "POST"
	Patch RequestMethod = "PATCH"
)

type HttpClient struct {
	backoffParams *backoff.ExponentialBackOff

	endpoint string
	body     []byte
	headers  map[string][]string
	params   map[string][]string
}

func New(endpoint string, body []byte, headers map[string][]string, params map[string][]string) *HttpClient {
	return &HttpClient{
		endpoint: endpoint,
		body:     body,
		headers:  headers,
		params:   params,
	}
}

func NewWithBackoff(endpoint string, body []byte, headers map[string][]string, params map[string][]string) *HttpClient {
	backoffParams := backoff.NewExponentialBackOff()

	// Ref: https://github.com/cenkalti/backoff/blob/a78d3804c2c84f0a3178648138442c9b07665bda/exponential.go#L76
	// DefaultInitialInterval     = 500 * time.Millisecond
	// DefaultRandomizationFactor = 0.5
	// DefaultMultiplier          = 1.5
	// DefaultMaxInterval         = 60 * time.Second
	// DefaultMaxElapsedTime      = 15 * time.Minute

	backoffParams.MaxInterval = 15 * time.Minute
	backoffParams.MaxElapsedTime = 72 * time.Hour

	return &HttpClient{
		backoffParams: backoffParams,
		endpoint:      endpoint,
		body:          body,
		headers:       headers,
		params:        params,
	}
}

func (h *HttpClient) request(method RequestMethod) (*http.Response, error) {
	if h.backoffParams == nil {
		return singleRequest()
	}

	ticker := backoff.NewTicker(h.backoffParams)

	// Keep looping through our ticker, waiting for it to tell us when to retry
	for range ticker.C {
		singleRequest()
	}
}

func (h *HttpClient) singleRequest(method RequestMethod) (*http.Response, error) {
	// Make our Client
	client := http.Client{
		Timeout: httpTimeout,
	}

	// Make our Request
	request, _ := http.NewRequest(string(method), b.endpoint, bytes.NewBuffer(b.body))
	request.Header = http.Header(b.headers)

	// Add params to request URL
	query := url.Values(s.params)
	request.URL.RawQuery = query.Encode()

	response, err = httpClient.Do(request)

	if err != nil {
		b.logger.Errorf("error making POST request: %s", err)
		continue
	} else if err := checkBadStatusCode(response); err != nil {
		ticker.Stop()
		return response, err
	} else if response.StatusCode >= 200 && response.StatusCode < 300 {
		ticker.Stop()
		return response, nil
	} else {
		b.logger.Errorf("Received status code %d making POST request, will retry in %s", response.StatusCode, b.backoffParams.NextBackOff().Round(time.Second))
		continue
	}
}

func (h *HttpClient) Post() (*http.Response, error) {

	ticker := backoff.NewTicker(h.backoffParams)

	// Keep looping through our ticker, waiting for it to tell us when to retry
	for range ticker.C {
		// Make our Client
		client := http.Client{
			Timeout: httpTimeout,
		}

		if len(b.headers) == 0 && len(b.params) == 0 {
			response, err = httpClient.Post(b.endpoint, b.contentType, bytes.NewBuffer(b.body))
		} else {
			// Make our Request
			req, _ := http.NewRequest("POST", b.endpoint, bytes.NewBuffer(b.body))
			req = addHeaders(req, b.headers, b.contentType)
			req = addQueryParams(req, b.params)

			response, err = httpClient.Do(req)
		}

		if err != nil {
			b.logger.Errorf("error making POST request: %s", err)
			continue
		} else if err := checkBadStatusCode(response); err != nil {
			ticker.Stop()
			return response, err
		} else if response.StatusCode >= 200 && response.StatusCode < 300 {
			ticker.Stop()
			return response, nil
		} else {
			b.logger.Errorf("Received status code %d making POST request, will retry in %s", response.StatusCode, b.backoffParams.NextBackOff().Round(time.Second))
			continue
		}
	}

	return nil, errors.New("unable to make post request")
}
