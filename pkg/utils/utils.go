package utils

import (
	"fmt"
	"net/url"
	"os"
)

func GetVideoID(youtubeURL string) (string, error) {
	parsedURL, err := url.Parse(youtubeURL)
	if err != nil {
		return "", err
	}

	queryParams := parsedURL.Query()
	videoID := queryParams.Get("v")

	if videoID == "" {
		return "", fmt.Errorf("video ID not found in URL")
	}

	return videoID, nil
}

func ExitOnError(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
