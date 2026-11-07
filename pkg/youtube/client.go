// Package youtube is a small, dependency-free YouTube extractor and
// downloader. The extraction logic follows yt-dlp's YouTube extractor: it
// asks the InnerTube player API for the streams using the visionOS client,
// which (as of yt-dlp 2026.10) needs no login, no PO token and no JavaScript
// challenge solving, so stream URLs can be used as returned.
package youtube

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

// innertubeClient mirrors an entry of yt-dlp's INNERTUBE_CLIENTS table.
type innertubeClient struct {
	name    string // context.client.clientName
	id      int    // X-YouTube-Client-Name
	version string
	context map[string]any
	ua      string
}

var visionOS = innertubeClient{
	name:    "VISIONOS",
	id:      101,
	version: "1.02",
	ua:      "Mozilla/5.0 (Macintosh; Intel Mac OS X 15_7_3) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/26.0 Safari/605.1.15",
	context: map[string]any{
		"deviceMake":  "Apple",
		"deviceModel": "RealityDevice17,1",
		"osName":      "visionOS",
		"osVersion":   "26.5.23O471",
	},
}

const (
	origin     = "https://www.youtube.com"
	browserUA  = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"
	apiTimeout = 20 * time.Second
)

// Client talks to YouTube. The zero value is not usable; call NewClient.
type Client struct {
	HTTP *http.Client
	// Connections is the number of parallel connections used by Download.
	Connections int
}

// NewClient returns a client tuned for parallel downloads.
func NewClient(connections int) *Client {
	if connections < 1 {
		connections = 8
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = connections * 2
	// Separate TCP connections download faster from googlevideo than
	// multiplexed HTTP/2 streams sharing one connection.
	tr.ForceAttemptHTTP2 = false
	tr.TLSClientConfig = &tls.Config{NextProtos: []string{"http/1.1"}}
	tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	return &Client{HTTP: &http.Client{Transport: tr}, Connections: connections}
}

// Video is the metadata and stream list of one video.
type Video struct {
	ID       string
	Title    string
	Author   string
	Duration time.Duration
	Formats  []Format
}

// PlayabilityError is returned when YouTube refuses to serve the video.
type PlayabilityError struct {
	Status string
	Reason string
}

func (e *PlayabilityError) Error() string {
	if e.Reason == "" {
		return "video unavailable (" + e.Status + ")"
	}
	return "video unavailable: " + e.Reason
}

type playerResponse struct {
	PlayabilityStatus struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	} `json:"playabilityStatus"`
	VideoDetails struct {
		VideoID       string `json:"videoId"`
		Title         string `json:"title"`
		Author        string `json:"author"`
		LengthSeconds string `json:"lengthSeconds"`
		IsLive        bool   `json:"isLive"`
		IsLiveContent bool   `json:"isLiveContent"`
	} `json:"videoDetails"`
	StreamingData struct {
		AdaptiveFormats []rawFormat `json:"adaptiveFormats"`
	} `json:"streamingData"`
}

// GetVideo fetches the video metadata and the available streams.
func (c *Client) GetVideo(ctx context.Context, id string) (*Video, error) {
	visitor, err := c.visitorData(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not start a YouTube session: %w", err)
	}

	pr, err := c.player(ctx, visionOS, id, visitor)
	if err != nil {
		return nil, err
	}
	if s := pr.PlayabilityStatus; s.Status != "OK" {
		return nil, &PlayabilityError{Status: s.Status, Reason: s.Reason}
	}
	if pr.VideoDetails.VideoID != id {
		return nil, fmt.Errorf("YouTube returned a different video (%q)", pr.VideoDetails.VideoID)
	}
	if pr.VideoDetails.IsLive {
		return nil, errors.New("live streams are not supported")
	}

	secs, _ := strconv.Atoi(pr.VideoDetails.LengthSeconds)
	v := &Video{
		ID:       id,
		Title:    pr.VideoDetails.Title,
		Author:   pr.VideoDetails.Author,
		Duration: time.Duration(secs) * time.Second,
	}
	for _, rf := range pr.StreamingData.AdaptiveFormats {
		if f, ok := rf.parse(); ok {
			v.Formats = append(v.Formats, f)
		}
	}
	if len(v.Formats) == 0 {
		return nil, errors.New("YouTube returned no downloadable streams for this video")
	}
	return v, nil
}

func (c *Client) player(ctx context.Context, ic innertubeClient, id, visitor string) (*playerResponse, error) {
	client := map[string]any{
		"clientName":    ic.name,
		"clientVersion": ic.version,
		"userAgent":     ic.ua,
		"hl":            "en",
		"gl":            "US",
		"visitorData":   visitor,
	}
	for k, v := range ic.context {
		client[k] = v
	}
	body, _ := json.Marshal(map[string]any{
		"context": map[string]any{"client": client},
		"videoId": id,
		"playbackContext": map[string]any{
			"contentPlaybackContext": map[string]any{"html5Preference": "HTML5_PREF_WANTS"},
		},
		"contentCheckOk": true,
		"racyCheckOk":    true,
	})

	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/youtubei/v1/player?prettyPrint=false", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", ic.ua)
	req.Header.Set("Origin", origin)
	req.Header.Set("X-YouTube-Client-Name", strconv.Itoa(ic.id))
	req.Header.Set("X-YouTube-Client-Version", ic.version)
	if visitor != "" {
		req.Header.Set("X-Goog-Visitor-Id", visitor)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("player request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("player request failed: HTTP %d", resp.StatusCode)
	}
	var pr playerResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, fmt.Errorf("could not parse player response: %w", err)
	}
	return &pr, nil
}

var visitorRe = regexp.MustCompile(`"VISITOR_DATA":"([^"]+)"`)

// visitorData returns an anonymous session ID. Without it YouTube answers
// with "Sign in to confirm you're not a bot". sw.js_data is a ~3 KB response,
// much lighter than the 1+ MB watch page yt-dlp downloads for the same value;
// the watch page is kept as a fallback.
func (c *Client) visitorData(ctx context.Context) (string, error) {
	raw, err := c.get(ctx, origin+"/sw.js_data")
	if err == nil {
		var data any
		if json.Unmarshal(bytes.TrimPrefix(raw, []byte(")]}'")), &data) == nil {
			if s, ok := dig(data, 0, 2, 0, 0, 13).(string); ok && s != "" {
				return s, nil
			}
		}
	}

	raw, err = c.get(ctx, origin+"/")
	if err != nil {
		return "", err
	}
	if m := visitorRe.FindSubmatch(raw); m != nil {
		return string(m[1]), nil
	}
	return "", errors.New("visitor data not found")
}

func (c *Client) get(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// dig walks nested JSON arrays by index.
func dig(v any, path ...int) any {
	for _, i := range path {
		arr, ok := v.([]any)
		if !ok || i >= len(arr) {
			return nil
		}
		v = arr[i]
	}
	return v
}
