package utils

import (
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"path"
)

func JoinUrls(base string, toAdd string) (string, error) {
	urlObject, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	urlObject.Path = path.Join(urlObject.Path, toAdd)
	return urlObject.String(), nil
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
