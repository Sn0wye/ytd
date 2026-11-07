package youtube

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// ErrInvalidID is returned when no video ID can be found in the input.
var ErrInvalidID = errors.New("could not find a YouTube video ID in the given URL")

// ExtractVideoID accepts a bare 11-character ID or any common YouTube URL
// (watch, youtu.be, shorts, embed, live, music, nocookie) and returns the ID.
func ExtractVideoID(input string) (string, error) {
	input = strings.TrimSpace(input)
	if idPattern.MatchString(input) {
		return input, nil
	}
	if !strings.Contains(input, "://") {
		input = "https://" + input
	}
	u, err := url.Parse(input)
	if err != nil {
		return "", ErrInvalidID
	}

	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	host = strings.TrimPrefix(host, "m.")
	segments := strings.Split(strings.Trim(u.Path, "/"), "/")

	var id string
	switch host {
	case "youtu.be":
		id = segments[0]
	case "youtube.com", "music.youtube.com", "youtube-nocookie.com":
		if v := u.Query().Get("v"); v != "" {
			id = v
		} else if len(segments) >= 2 {
			switch segments[0] {
			case "shorts", "embed", "live", "v", "e":
				id = segments[1]
			}
		}
	}
	if !idPattern.MatchString(id) {
		return "", ErrInvalidID
	}
	return id, nil
}
