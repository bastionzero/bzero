package utils

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
)

func MatchRequestId(requestIdPassed string, requestIdSaved string) error {
	if requestIdPassed != requestIdSaved {
		rerr := fmt.Errorf("invalid request ID passed: %s", requestIdPassed)
		return rerr
	}
	return nil
}

func BuildHttpRequest(kubeHost string, endpoint string, body string, method string, headers map[string][]string, serviceAccountToken string, targetUser string, targetGroups []string) (*http.Request, error) {
	// Perform the api request
	kubeApiUrl := kubeHost + endpoint
	bodyBytesReader := bytes.NewReader([]byte(body))
	req, _ := http.NewRequest(method, kubeApiUrl, bodyBytesReader)

	// First sanitize any headers
	headers = cleanHeaders(headers)

	// Add any headers
	for name, values := range headers {
		// Loop over all values for the name.
		for _, value := range values {
			req.Header.Set(name, value)
		}
	}

	// Add our impersonation and token headers
	req.Header.Set("Authorization", "Bearer "+serviceAccountToken)
	req.Header.Set("Impersonate-User", targetUser)
	for _, impersonateGroup := range targetGroups {
		req.Header.Set("Impersonate-Group", impersonateGroup)
	}

	// Always ensure that our Impersonate-User field is set
	// This is to ensure that we never run api calls as the underlying service account
	if req.Header.Get("Impersonate-User") == "" {
		rerr := fmt.Errorf("target user field is not set")
		return nil, rerr
	}

	// TODO: Figure out a way around this
	// CA certs can be found here /var/run/secrets/kubernetes.io/serviceaccount/ca.crt
	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	return req, nil
}

func cleanHeaders(headers map[string][]string) map[string][]string {
	// Function to clean our headers to remove any malicious headers
	// Ref: https://github.com/rancher/rancher/commit/5506ffe90245b23466ad1cb452c4346bb8aa4a9d#

	for headerName := range headers {
		// Also check if they are trying to add a impersonate-extra-(some extra)
		// Ref: https://kubernetes.io/docs/reference/access-authn-authz/authentication/#user-impersonation
		if strings.HasPrefix(headerName, "Impersonate-Extra-") {
			delete(headers, headerName)
		}

		// Check if they are trying to impersonate a uid, we do not support that
		// Ref: https://github.com/kubernetes/kubernetes/pull/99961
		if strings.ToLower(headerName) == "impersonate-uid" {
			delete(headers, headerName)
		}

	}

	// Once we are all done, return the updated headers
	return headers
}
