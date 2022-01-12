package utils

import (
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
