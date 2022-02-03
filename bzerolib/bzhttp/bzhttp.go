package bzhttp

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"bastionzero.com/bctl/v1/bzerolib/logger"
	backoff "github.com/cenkalti/backoff/v4"
)

type bzhttp struct {
	logger        *logger.Logger
	endpoint      string
	contentType   string
	body          []byte
	headers       map[string]string
	params        map[string]string
	backoffParams backoff.BackOff
}

func BuildEndpoint(base string, toAdd string) (string, error) {
	urlObject, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	urlObject.Path = path.Join(urlObject.Path, toAdd)

	// There is a problem with path.Join where it interally calls a Clean(..) function
	// which will remove any trailing slashes, this causes issues when proxying requests
	// that are expecting the trailing slash.
	// Ref: https://forum.golangbridge.org/t/how-to-concatenate-paths-for-api-request/5791
	toReturn := urlObject.String()
	if strings.HasSuffix(toAdd, "/") {
		toReturn += "/"
	}

	return toReturn, nil
}

// Helper function to extract the body of a http request
func GetBodyBytes(body io.ReadCloser) ([]byte, error) {
	bodyInBytes, err := ioutil.ReadAll(body)
	if err != nil {
		rerr := fmt.Errorf("error building body: %s", err)
		return nil, rerr
	}
	return bodyInBytes, nil
}

// Helper function to extract headers from a http request
func GetHeaders(headers http.Header) map[string][]string {
	toReturn := make(map[string][]string)
	for name, values := range headers {
		toReturn[name] = values
	}
	return toReturn
}

func PostContent(logger *logger.Logger, endpoint string, contentType string, body []byte) (*http.Response, error) {
	return Post(logger, endpoint, contentType, body, make(map[string]string), make(map[string]string))
}

func Post(logger *logger.Logger, endpoint string, contentType string, body []byte, headers map[string]string, params map[string]string) (*http.Response, error) {
	// Helper function to perform exponential backoff on http post requests

	// Define our exponential backoff params
	backoffParams := backoff.NewExponentialBackOff()
	backoffParams.MaxElapsedTime = time.Hour * 8 // Wait in total at most 8 hours

	req := &bzhttp{
		logger:        logger,
		endpoint:      endpoint,
		contentType:   contentType,
		body:          body,
		headers:       headers,
		params:        params,
		backoffParams: backoffParams,
	}

	return req.post()

}

func Get(logger *logger.Logger, endpoint string, headers map[string]string, params map[string]string) (*http.Response, error) {
	// Helper function to perform exponential backoff on http get requests

	// Define our exponential backoff params
	backoffParams := backoff.NewExponentialBackOff()
	backoffParams.MaxElapsedTime = time.Hour * 8 // Wait in total at most 8 hours

	req := &bzhttp{
		logger:        logger,
		endpoint:      endpoint,
		contentType:   "",
		body:          []byte{},
		headers:       headers,
		params:        params,
		backoffParams: backoffParams,
	}

	return req.get()
}

func PostRegister(logger *logger.Logger, endpoint string, contentType string, body []byte) (*http.Response, error) {
	// For the registration post request, we set different parameters for our exponential backoff

	// Define our exponential backoff params
	params := backoff.NewExponentialBackOff()
	params.MaxElapsedTime = time.Hour * 4 // Wait in total at most 4 hours
	params.MaxInterval = time.Hour        // At most 1 hour in between requests

	req := &bzhttp{
		logger:        logger,
		endpoint:      endpoint,
		contentType:   contentType,
		body:          body,
		headers:       make(map[string]string),
		params:        make(map[string]string),
		backoffParams: params,
	}

	return req.post()
}

func (b *bzhttp) post() (*http.Response, error) {
	// Default params
	// Ref: https://github.com/cenkalti/backoff/blob/a78d3804c2c84f0a3178648138442c9b07665bda/exponential.go#L76
	// DefaultInitialInterval     = 500 * time.Millisecond
	// DefaultRandomizationFactor = 0.5
	// DefaultMultiplier          = 1.5
	// DefaultMaxInterval         = 60 * time.Second
	// DefaultMaxElapsedTime      = 15 * time.Minute

	// Make our ticker
	ticker := backoff.NewTicker(b.backoffParams)

	// Keep looping through our ticker, waiting for it to tell us when to retry
	for range ticker.C {
		// Make our Client
		var httpClient = &http.Client{
			Timeout: time.Second * 10,
		}

		// declare our variables
		var response *http.Response
		var err error

		if len(b.headers) == 0 && len(b.params) == 0 {
			response, err = httpClient.Post(b.endpoint, b.contentType, bytes.NewBuffer(b.body))

			if err != nil {
				b.logger.Errorf("error making post request: %v", err)
				return nil, err
			}
		} else {
			// Make our Request
			req, _ := http.NewRequest("POST", b.endpoint, bytes.NewBuffer(b.body))

			// Add the expected headers
			for name, values := range b.headers {
				// Loop over all values for the name.
				req.Header.Set(name, values)
			}

			// Add the content type header
			req.Header.Set("Content-Type", b.contentType)

			// Set any query params
			q := req.URL.Query()
			for key, values := range b.params {
				q.Add(key, values)
			}

			q.Add("clientProtocol", "1.5")
			req.URL.RawQuery = q.Encode()

			response, err = httpClient.Do(req)

			if err != nil {
				b.logger.Errorf("error making post request: %v", err)
				return nil, err
			}
		}

		// If the status code is unauthorized, do not attempt to retry
		if response.StatusCode == http.StatusInternalServerError || response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusUnsupportedMediaType {
			ticker.Stop()
			return response, fmt.Errorf("received response code: %d, not retrying", response.StatusCode)
		}

		if err != nil || response.StatusCode != http.StatusOK {
			b.logger.Infof("error making post request %v/%v, will retry in: %s.", err, response, b.backoffParams.NextBackOff())

			bodyBytes, err := io.ReadAll(response.Body)
			if err != nil {
				b.logger.Error(err)
			}
			bodyString := string(bodyBytes)
			b.logger.Infof("error: %s", bodyString)
			continue
		}

		ticker.Stop()
		return response, err
	}

	return nil, errors.New("unable to make post request")
}

func (b *bzhttp) get() (*http.Response, error) {
	// Default params
	// Ref: https://github.com/cenkalti/backoff/blob/a78d3804c2c84f0a3178648138442c9b07665bda/exponential.go#L76
	// DefaultInitialInterval     = 500 * time.Millisecond
	// DefaultRandomizationFactor = 0.5
	// DefaultMultiplier          = 1.5
	// DefaultMaxInterval         = 60 * time.Second
	// DefaultMaxElapsedTime      = 15 * time.Minute

	// Make our ticker
	ticker := backoff.NewTicker(b.backoffParams)

	// Keep looping through our ticker, waiting for it to tell us when to retry
	for range ticker.C {
		// Make our Client
		var httpClient = &http.Client{
			Timeout: time.Second * 10,
		}

		// declare our variables
		var response *http.Response
		var err error

		if len(b.headers) == 0 && len(b.params) == 0 {
			response, err = httpClient.Get(b.endpoint)
		} else {
			// Make our Request
			req, _ := http.NewRequest("GET", b.endpoint, bytes.NewBuffer(b.body))

			// Add the expected headers
			for name, values := range b.headers {
				// Loop over all values for the name.
				req.Header.Set(name, values)
			}

			// Add the content type header
			req.Header.Set("Content-Type", b.contentType)

			// Set any query params
			q := req.URL.Query()
			for key, values := range b.params {
				q.Add(key, values)
			}

			q.Add("clientProtocol", "1.5")
			req.URL.RawQuery = q.Encode()

			response, err = httpClient.Do(req)
		}

		// If the status code is unauthorized, do not attempt to retry
		if response.StatusCode == http.StatusInternalServerError || response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusNotFound {
			ticker.Stop()
			return response, fmt.Errorf("received response code: %d, not retrying", response.StatusCode)
		}

		if err != nil || response.StatusCode != http.StatusOK {
			b.logger.Infof("error making post request %v/%v, will retry in: %s.", err, response, b.backoffParams.NextBackOff())

			bodyBytes, err := io.ReadAll(response.Body)
			if err != nil {
				log.Fatal(err)
			}
			bodyString := string(bodyBytes)
			b.logger.Infof("error: %s", bodyString)
			continue
		}

		ticker.Stop()
		return response, err
	}

	return nil, errors.New("unable to make get request")
}
