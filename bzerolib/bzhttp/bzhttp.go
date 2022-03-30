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

	// Now undo any url encoding that might have happened in UrlObject.String()
	decodedUrl, err := url.QueryUnescape(urlObject.String())
	if err != nil {
		return "", err
	}

	// There is a problem with path.Join where it interally calls a Clean(..) function
	// which will remove any trailing slashes, this causes issues when proxying requests
	// that are expecting the trailing slash.
	// Ref: https://forum.golangbridge.org/t/how-to-concatenate-paths-for-api-request/5791
	if strings.HasSuffix(toAdd, "/") && !strings.HasSuffix(decodedUrl, "/") {
		decodedUrl += "/"
	}

	return decodedUrl, nil
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

	req := createBzhttp(logger, endpoint, contentType, headers, params, body, backoffParams)

	return req.post()

}

func Get(logger *logger.Logger, endpoint string, headers map[string]string, params map[string]string) (*http.Response, error) {
	// Helper function to perform exponential backoff on http get requests

	// Define our exponential backoff params
	backoffParams := backoff.NewExponentialBackOff()
	backoffParams.MaxElapsedTime = time.Hour * 8 // Wait in total at most 8 hours

	req := createBzhttp(logger, endpoint, "", headers, params, []byte{}, backoffParams)

	return req.get()
}

func Patch(logger *logger.Logger, endpoint string, headers map[string]string, params map[string]string) (*http.Response, error) {
	// Define our exponential backoff params
	backoffParams := backoff.NewExponentialBackOff()
	backoffParams.MaxElapsedTime = time.Hour * 8 // Wait in total at most 8 hours

	req := createBzhttp(logger, endpoint, "application/json", headers, params, []byte{}, backoffParams)

	return req.patch()
}

func createBzhttp(logger *logger.Logger, endpoint string, contentType string, headers map[string]string, params map[string]string, body []byte, backoffParams *backoff.ExponentialBackOff) bzhttp {
	return bzhttp{
		logger:        logger,
		endpoint:      endpoint,
		contentType:   contentType,
		body:          body,
		headers:       headers,
		params:        params,
		backoffParams: backoffParams,
	}
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

func PostNegotiate(logger *logger.Logger, endpoint string, contentType string, body []byte, headers map[string]string, params map[string]string) (*http.Response, error) {
	// minimal backoff for negotiate requests since they are wrapped in their own backoff logic

	// Define our exponential backoff params
	backoffParams := backoff.NewExponentialBackOff()
	backoffParams.MaxElapsedTime = time.Second * 10 // Wait in total at most 10 seconds

	req := createBzhttp(logger, endpoint, contentType, headers, params, body, backoffParams)

	return req.post()

}

func (b *bzhttp) patch() (*http.Response, error) {
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
		var httpClient = getHttpClient()

		// declare our variables
		var response *http.Response
		var err error

		// Make our Request
		req, _ := http.NewRequest("PATCH", b.endpoint, bytes.NewBuffer(b.body))

		// Add the expected headers
		req = addHeaders(req, b.headers, b.contentType)

		// Set any query params
		req = addQueryParams(req, b.params)

		response, err = httpClient.Do(req)

		if err != nil {
			b.logger.Errorf("error making patch request: %v", err)
			return nil, err
		}

		if err := checkBadStatusCode(response); err != nil {
			ticker.Stop()
			return response, err
		}

		if err != nil || response.StatusCode != http.StatusOK {
			b.logger.Infof("error making patch request %v/%v, will retry in: %s.", err, response, b.backoffParams.NextBackOff())

			bodyString := extractBody(response)
			b.logger.Infof("error: %s", bodyString)
			continue
		}

		ticker.Stop()
		return response, err
	}

	return nil, errors.New("unable to make post request")
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
		var httpClient = getHttpClient()

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
			req = addHeaders(req, b.headers, b.contentType)

			// Set any query params
			req = addQueryParams(req, b.params)

			response, err = httpClient.Do(req)

			if err != nil {
				b.logger.Errorf("error making post request: %v", err)
				return nil, err
			}
		}

		// If the status code is unauthorized, do not attempt to retry
		if err := checkBadStatusCode(response); err != nil {
			ticker.Stop()
			return response, err
		}

		if err != nil || response.StatusCode != http.StatusOK {
			b.logger.Infof("error making post request %v/%v, will retry in: %s.", err, response, b.backoffParams.NextBackOff())

			bodyString := extractBody(response)
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
		var httpClient = getHttpClient()

		// declare our variables
		var response *http.Response
		var err error

		if len(b.headers) == 0 && len(b.params) == 0 {
			response, err = httpClient.Get(b.endpoint)
		} else {
			// Make our Request
			req, _ := http.NewRequest("GET", b.endpoint, bytes.NewBuffer(b.body))

			// Add the expected headers
			req = addHeaders(req, b.headers, b.contentType)

			// Set any query params
			req = addQueryParams(req, b.params)

			response, err = httpClient.Do(req)
		}

		if err := checkBadStatusCode(response); err != nil {
			ticker.Stop()
			return response, err
		}

		if err != nil || response.StatusCode != http.StatusOK {
			b.logger.Infof("error making get request %v/%v, will retry in: %s.", err, response, b.backoffParams.NextBackOff())

			bodyString := extractBody(response)
			b.logger.Infof("error: %s", bodyString)
			continue
		}

		ticker.Stop()
		return response, err
	}

	return nil, errors.New("unable to make get request")
}

// Helper function to check if we received a status code that we should not attempt to try again
func checkBadStatusCode(response *http.Response) error {
	if response.StatusCode == http.StatusNotFound ||
		response.StatusCode == http.StatusUnauthorized ||
		response.StatusCode == http.StatusUnsupportedMediaType ||
		response.StatusCode == http.StatusGone {
		return fmt.Errorf("received response code: %d, not retrying", response.StatusCode)
	}
	return nil
}

// Helper function to add headers and set the content type
func addHeaders(request *http.Request, headers map[string]string, contentType string) *http.Request {
	// Add the expected headers
	for name, values := range headers {
		// Loop over all values for the name.
		request.Header.Set(name, values)
	}

	// Add the content type header
	request.Header.Set("Content-Type", contentType)

	return request
}

// Helper function to add query params
func addQueryParams(request *http.Request, params map[string]string) *http.Request {
	// Set any query params
	q := request.URL.Query()
	for key, values := range params {
		q.Add(key, values)
	}

	// Add the client protocol for signalr
	q.Add("clientProtocol", "1.5")
	request.URL.RawQuery = q.Encode()

	return request
}

// Helper function to build a http client
func getHttpClient() *http.Client {
	return &http.Client{
		Timeout: time.Second * 30,
	}
}

// Helper function to extract the response body
func extractBody(response *http.Response) string {
	bodyBytes, err := io.ReadAll(response.Body)
	if err != nil {
		log.Fatal(err)
	}
	return string(bodyBytes)
}
