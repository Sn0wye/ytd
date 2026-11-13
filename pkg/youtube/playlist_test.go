package youtube

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParsePlaylistPage(t *testing.T) {
	p, token, err := parsePlaylistPage(readTestdata(t, "playlist_page.json"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Uploads from Preto no Branco" {
		t.Errorf("title = %q", p.Title)
	}
	wantIDs := []string{"6H54Xx9U6uk", "n8QLOCqyRLw", "NNsXXC9vB3c"}
	if len(p.Entries) != len(wantIDs) {
		t.Fatalf("got %d entries, want %d: %+v", len(p.Entries), len(wantIDs), p.Entries)
	}
	for i, id := range wantIDs {
		if p.Entries[i].ID != id {
			t.Errorf("entry %d = %q, want %q", i, p.Entries[i].ID, id)
		}
	}
	if p.Entries[0].Duration != time.Minute || p.Entries[2].Duration != 49*time.Second {
		t.Errorf("durations = %v, %v", p.Entries[0].Duration, p.Entries[2].Duration)
	}
	if !strings.HasPrefix(p.Entries[2].Title, "Aquieta nossa alma") {
		t.Errorf("title = %q", p.Entries[2].Title)
	}
	if p.Skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the private video)", p.Skipped)
	}
	// The token comes from the list, not from the section-level
	// continuation that leads to recommendations.
	if token == "" || strings.Contains(token, "SECTION-LEVEL") {
		t.Errorf("token = %q", token)
	}
}

func TestParseContinuation(t *testing.T) {
	entries, skipped, token, err := parseContinuation(readTestdata(t, "playlist_continuation.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].ID != "GldASXOONuE" || skipped != 0 || token != "" {
		t.Errorf("got %d entries (%+v), skipped %d, token %q", len(entries), entries, skipped, token)
	}

	// After the last page YouTube appends a shelf of other playlists; none of
	// it is a playlist entry.
	entries, _, token, err = parseContinuation(readTestdata(t, "playlist_recommendations.json"))
	if err != nil || len(entries) != 0 || token != "" {
		t.Errorf("recommendations: got %+v, token %q, err %v", entries, token, err)
	}
}

func TestParseLegacyRenderer(t *testing.T) {
	raw := `{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"content":{"sectionListRenderer":{"contents":[
	 {"itemSectionRenderer":{"contents":[{"playlistVideoListRenderer":{"contents":[
	  {"playlistVideoRenderer":{"videoId":"aqz-KE-bpKQ","title":{"runs":[{"text":"Big Buck Bunny"}]},"lengthSeconds":"634","isPlayable":true}},
	  {"playlistVideoRenderer":{"videoId":"xxxxxxxxxxx","title":{"runs":[{"text":"[Deleted video]"}]},"isPlayable":false}},
	  {"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"NEXT"}}}}
	 ]}}]}}]}}}}]}},
	 "header":{"playlistHeaderRenderer":{"title":{"simpleText":"Legacy"},"ownerText":{"runs":[{"text":"Owner"}]}}}}`
	p, token, err := parsePlaylistPage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Legacy" || p.Author != "Owner" || len(p.Entries) != 1 || p.Entries[0].Duration != 634*time.Second || p.Skipped != 1 || token != "NEXT" {
		t.Errorf("got %+v, token %q", p, token)
	}
}

func TestParsePlaylistUnavailable(t *testing.T) {
	raw := `{"alerts":[{"alertRenderer":{"type":"ERROR","text":{"runs":[{"text":"The playlist does not exist."}]}}}]}`
	_, _, err := parsePlaylistPage([]byte(raw))
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("err = %v", err)
	}
}

func TestParseClock(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"6:33": 6*time.Minute + 33*time.Second, "1:02:03": time.Hour + 2*time.Minute + 3*time.Second, "": 0, "LIVE": 0,
	} {
		if got := parseClock(in); got != want {
			t.Errorf("parseClock(%q) = %v, want %v", in, got, want)
		}
	}
}

// fakeYouTube answers sw.js_data and the browse API from fixtures.
type fakeYouTube struct {
	t        *testing.T
	browses  []map[string]any
	pages    map[string][]byte // by browseId or continuation token
	visitors []string
}

func (f *fakeYouTube) RoundTrip(r *http.Request) (*http.Response, error) {
	respond := func(b []byte) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b))), Header: http.Header{}, Request: r}, nil
	}
	switch r.URL.Path {
	case "/sw.js_data":
		return respond([]byte(`)]}'[[0,0,[[[0,0,0,0,0,0,0,0,0,0,0,0,0,"VISITOR"]]]]]`))
	case "/youtubei/v1/browse":
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			f.t.Fatal(err)
		}
		f.browses = append(f.browses, req)
		f.visitors = append(f.visitors, r.Header.Get("X-Goog-Visitor-Id"))
		key, _ := req["browseId"].(string)
		if key == "" {
			key, _ = req["continuation"].(string)
		}
		if b, ok := f.pages[key]; ok {
			return respond(b)
		}
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}, Request: r}, nil
	}
	f.t.Fatalf("unexpected request %s", r.URL)
	return nil, nil
}

func TestGetPlaylist(t *testing.T) {
	page := readTestdata(t, "playlist_page.json")
	_, token, err := parsePlaylistPage(page)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeYouTube{t: t, pages: map[string][]byte{
		"VLUUlzJiwPb3KUnmQEscBtEpRA": page,
		token:                        readTestdata(t, "playlist_continuation.json"),
	}}
	c := NewClient(1)
	c.HTTP = &http.Client{Transport: fake}

	p, err := c.GetPlaylist(context.Background(), "UUlzJiwPb3KUnmQEscBtEpRA")
	if err != nil {
		t.Fatal(err)
	}
	// 3 from the first page + 2 new ones (the third continuation entry is a
	// duplicate of the first).
	if len(p.Entries) != 5 || p.ID != "UUlzJiwPb3KUnmQEscBtEpRA" || p.Skipped != 1 {
		t.Errorf("got %d entries, id %q, skipped %d", len(p.Entries), p.ID, p.Skipped)
	}
	if len(fake.browses) != 2 || fake.visitors[0] != "VISITOR" {
		t.Errorf("browse calls = %d, visitor %q", len(fake.browses), fake.visitors)
	}

	if _, err := c.GetPlaylist(context.Background(), "PLmissingmissing"); err == nil {
		t.Error("missing playlist: want error")
	}
}
