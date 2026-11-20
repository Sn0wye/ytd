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

// listPattern matches the playlist IDs the browse API can list: user
// playlists (PL), albums and other generated lists (OL, OLAK5uy_), channel
// uploads (UU and its variants), liked videos (LL) and favourites (FL).
// Mixes (RD...) are generated on the fly and cannot be listed this way.
var listPattern = regexp.MustCompile(`^(PL|OL|UU|UUSH|UULF|UUMO|UULV|UUPV|UUPS|UUMF|FL|LL|PU)[A-Za-z0-9_-]{10,}$`)

// ErrInvalidPlaylist is returned when no playlist ID can be found in the input.
var ErrInvalidPlaylist = errors.New("could not find a YouTube playlist ID in the given URL")

// ExtractPlaylistID accepts a bare playlist ID or any YouTube URL with a
// list= parameter (playlist, watch, music) and returns the playlist ID.
func ExtractPlaylistID(input string) (string, error) {
	input = strings.TrimSpace(input)
	if listPattern.MatchString(input) {
		return input, nil
	}
	if !strings.Contains(input, "://") {
		input = "https://" + input
	}
	u, err := url.Parse(input)
	if err != nil {
		return "", ErrInvalidPlaylist
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	host = strings.TrimPrefix(host, "m.")
	switch host {
	case "youtube.com", "music.youtube.com", "youtube-nocookie.com", "youtu.be":
	default:
		return "", ErrInvalidPlaylist
	}
	list := u.Query().Get("list")
	if strings.HasPrefix(list, "RD") {
		return "", errors.New("YouTube mixes (list=RD...) are generated per viewer and cannot be downloaded as a playlist")
	}
	if !listPattern.MatchString(list) {
		return "", ErrInvalidPlaylist
	}
	return list, nil
}

// IsPlaylistPage reports whether input points at a playlist itself (a
// /playlist?list= page or a bare playlist ID) rather than a video that
// happens to be played from a playlist.
func IsPlaylistPage(input string) bool {
	input = strings.TrimSpace(input)
	if listPattern.MatchString(input) {
		return true
	}
	if _, err := ExtractPlaylistID(input); err != nil {
		return false
	}
	_, err := ExtractVideoID(input)
	return err != nil
}
