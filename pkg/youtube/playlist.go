package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// webClient is the desktop web client, used only for the browse API. Its
// version follows the INNERTUBE_CLIENT_VERSION served on youtube.com.
var webClient = innertubeClient{
	name:    "WEB",
	id:      1,
	version: "2.20261112.00.00",
	ua:      browserUA,
}

// maxPlaylistPages stops runaway continuation loops (100 entries per page).
const maxPlaylistPages = 200

// Playlist is the title and the videos of a playlist, in order.
type Playlist struct {
	ID      string
	Title   string
	Author  string
	Entries []Entry
	// Skipped counts entries YouTube lists but will not play (private or
	// deleted videos).
	Skipped int
}

// Entry is one video of a playlist.
type Entry struct {
	ID       string
	Title    string
	Duration time.Duration
}

// GetPlaylist lists every video of the playlist with the given ID, following
// continuation pages until the end.
func (c *Client) GetPlaylist(ctx context.Context, listID string) (*Playlist, error) {
	visitor, err := c.visitorData(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not start a YouTube session: %w", err)
	}

	raw, err := c.browse(ctx, visitor, map[string]any{"browseId": "VL" + listID})
	if err != nil {
		return nil, err
	}
	p, token, err := parsePlaylistPage(raw)
	if err != nil {
		return nil, err
	}
	p.ID = listID

	seen := make(map[string]bool, len(p.Entries))
	for _, e := range p.Entries {
		seen[e.ID] = true
	}
	for page := 0; token != "" && page < maxPlaylistPages; page++ {
		raw, err := c.browse(ctx, visitor, map[string]any{"continuation": token})
		if err != nil {
			return nil, err
		}
		var entries []Entry
		var skipped int
		entries, skipped, token, err = parseContinuation(raw)
		if err != nil {
			return nil, err
		}
		p.Skipped += skipped
		for _, e := range entries {
			if !seen[e.ID] {
				seen[e.ID] = true
				p.Entries = append(p.Entries, e)
			}
		}
	}
	if token != "" {
		return nil, errors.New("playlist exceeds the continuation page limit")
	}
	if len(p.Entries) == 0 {
		return nil, errors.New("the playlist has no playable videos")
	}
	return p, nil
}

func (c *Client) browse(ctx context.Context, visitor string, params map[string]any) ([]byte, error) {
	ic := webClient
	req := map[string]any{
		"context": map[string]any{"client": map[string]any{
			"clientName":    ic.name,
			"clientVersion": ic.version,
			"userAgent":     ic.ua,
			"hl":            "en",
			"gl":            "US",
			"visitorData":   visitor,
		}},
	}
	for k, v := range params {
		req[k] = v
	}
	body, _ := json.Marshal(req)

	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/youtubei/v1/browse?prettyPrint=false", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("User-Agent", ic.ua)
	r.Header.Set("Origin", origin)
	r.Header.Set("X-YouTube-Client-Name", strconv.Itoa(ic.id))
	r.Header.Set("X-YouTube-Client-Version", ic.version)
	if visitor != "" {
		r.Header.Set("X-Goog-Visitor-Id", visitor)
	}
	resp, err := c.HTTP.Do(r)
	if err != nil {
		return nil, fmt.Errorf("playlist request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest {
		return nil, errors.New("playlist not found (it may be private or deleted)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("playlist request failed: HTTP %d", resp.StatusCode)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, fmt.Errorf("playlist request failed: %w", err)
	}
	return buf.Bytes(), nil
}

// The browse response is large and its shape changes over time, so it is
// walked as generic JSON. Only the list of the playlist itself is read:
// shelves of recommended playlists that YouTube appends after the last page
// are ignored.

type jsonObj = map[string]any

func parsePlaylistPage(raw []byte) (*Playlist, string, error) {
	var data jsonObj
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, "", fmt.Errorf("could not parse playlist response: %w", err)
	}
	if alert := alertText(data); alert != "" && path(data, "contents") == nil {
		return nil, "", fmt.Errorf("playlist unavailable: %s", alert)
	}

	p := &Playlist{
		Title:  str(path(data, "metadata", "playlistMetadataRenderer", "title")),
		Author: runsText(path(data, "header", "playlistHeaderRenderer", "ownerText")),
	}
	if p.Author == "" {
		sidebar, _ := path(data, "sidebar", "playlistSidebarRenderer", "items").([]any)
		for _, item := range sidebar {
			if author := runsText(path(item, "playlistSidebarSecondaryInfoRenderer", "videoOwner", "videoOwnerRenderer", "title")); author != "" {
				p.Author = author
				break
			}
		}
	}
	if p.Title == "" {
		p.Title = str(path(data, "header", "playlistHeaderRenderer", "title", "simpleText"))
	}

	tabs, _ := path(data, "contents", "twoColumnBrowseResultsRenderer", "tabs").([]any)
	if len(tabs) == 0 {
		if alert := alertText(data); alert != "" {
			return nil, "", fmt.Errorf("playlist unavailable: %s", alert)
		}
		return nil, "", errors.New("playlist not found (it may be private or deleted)")
	}
	sections, _ := path(tabs[0], "tabRenderer", "content", "sectionListRenderer", "contents").([]any)
	for _, s := range sections {
		items, ok := path(s, "itemSectionRenderer", "contents").([]any)
		if !ok {
			continue
		}
		// Older layout: the list sits in a playlistVideoListRenderer.
		if len(items) == 1 {
			if inner, ok := path(items[0], "playlistVideoListRenderer", "contents").([]any); ok {
				items = inner
			}
		}
		entries, skipped, token := parseItems(items)
		p.Entries, p.Skipped = entries, skipped
		return p, token, nil
	}
	return nil, "", errors.New("could not find the playlist videos in YouTube's response")
}

func parseContinuation(raw []byte) ([]Entry, int, string, error) {
	var data jsonObj
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, 0, "", fmt.Errorf("could not parse playlist response: %w", err)
	}
	actions, _ := data["onResponseReceivedActions"].([]any)
	for _, a := range actions {
		items, ok := path(a, "appendContinuationItemsAction", "continuationItems").([]any)
		if !ok {
			continue
		}
		entries, skipped, token := parseItems(items)
		return entries, skipped, token, nil
	}
	return nil, 0, "", nil
}

// parseItems reads the videos of one page and its continuation token.
func parseItems(items []any) (entries []Entry, skipped int, token string) {
	for _, it := range items {
		switch {
		case path(it, "lockupViewModel") != nil:
			l := path(it, "lockupViewModel")
			if str(path(l, "contentType")) != "LOCKUP_CONTENT_TYPE_VIDEO" {
				continue
			}
			e := Entry{
				ID:    str(path(l, "contentId")),
				Title: str(path(l, "metadata", "lockupMetadataViewModel", "title", "content")),
			}
			e.Duration = parseClock(lockupDuration(l))
			if !idPattern.MatchString(e.ID) || unavailableTitle(e.Title) {
				skipped++
				continue
			}
			entries = append(entries, e)

		case path(it, "playlistVideoRenderer") != nil:
			r := path(it, "playlistVideoRenderer")
			e := Entry{ID: str(path(r, "videoId")), Title: runsText(path(r, "title"))}
			if secs, err := strconv.Atoi(str(path(r, "lengthSeconds"))); err == nil {
				e.Duration = time.Duration(secs) * time.Second
			}
			if playable, ok := path(r, "isPlayable").(bool); (ok && !playable) || !idPattern.MatchString(e.ID) || unavailableTitle(e.Title) {
				skipped++
				continue
			}
			entries = append(entries, e)

		case path(it, "continuationItemViewModel") != nil:
			token = str(path(it, "continuationItemViewModel", "continuationCommand", "innertubeCommand", "continuationCommand", "token"))

		case path(it, "continuationItemRenderer") != nil:
			token = str(path(it, "continuationItemRenderer", "continuationEndpoint", "continuationCommand", "token"))
		}
	}
	return entries, skipped, token
}

func unavailableTitle(t string) bool {
	return t == "[Private video]" || t == "[Deleted video]"
}

// lockupDuration finds the "6:33" badge on the thumbnail.
func lockupDuration(l any) string {
	overlays, _ := path(l, "contentImage", "thumbnailViewModel", "overlays").([]any)
	for _, o := range overlays {
		badges, _ := path(o, "thumbnailBottomOverlayViewModel", "badges").([]any)
		for _, b := range badges {
			if t := str(path(b, "thumbnailBadgeViewModel", "text")); strings.Contains(t, ":") {
				return t
			}
		}
	}
	return ""
}

// parseClock parses "6:33" or "1:02:03".
func parseClock(s string) time.Duration {
	var total int
	for _, part := range strings.Split(s, ":") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return 0
		}
		total = total*60 + n
	}
	return time.Duration(total) * time.Second
}

func alertText(data jsonObj) string {
	alerts, _ := data["alerts"].([]any)
	for _, a := range alerts {
		for _, key := range []string{"alertRenderer", "alertWithButtonRenderer"} {
			if t := runsText(path(a, key, "text")); t != "" {
				return t
			}
		}
	}
	return ""
}

// path walks nested JSON objects by key.
func path(v any, keys ...string) any {
	for _, k := range keys {
		m, ok := v.(jsonObj)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// runsText reads {"simpleText": ...} or {"runs": [{"text": ...}]}.
func runsText(v any) string {
	if s := str(path(v, "simpleText")); s != "" {
		return s
	}
	runs, _ := path(v, "runs").([]any)
	var b strings.Builder
	for _, r := range runs {
		b.WriteString(str(path(r, "text")))
	}
	return b.String()
}
