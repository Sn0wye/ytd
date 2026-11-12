package youtube

import "testing"

func TestExtractVideoID(t *testing.T) {
	const want = "aqz-KE-bpKQ"
	ok := []string{
		"aqz-KE-bpKQ",
		"https://www.youtube.com/watch?v=aqz-KE-bpKQ",
		"https://www.youtube.com/watch?v=aqz-KE-bpKQ&t=42s&list=PL123",
		"https://m.youtube.com/watch?v=aqz-KE-bpKQ",
		"https://music.youtube.com/watch?v=aqz-KE-bpKQ&feature=share",
		"youtube.com/watch?v=aqz-KE-bpKQ",
		"https://youtu.be/aqz-KE-bpKQ",
		"https://youtu.be/aqz-KE-bpKQ?si=abc",
		"https://www.youtube.com/shorts/aqz-KE-bpKQ",
		"https://www.youtube.com/embed/aqz-KE-bpKQ",
		"https://www.youtube.com/live/aqz-KE-bpKQ?feature=share",
		"https://www.youtube-nocookie.com/embed/aqz-KE-bpKQ",
	}
	for _, in := range ok {
		got, err := ExtractVideoID(in)
		if err != nil || got != want {
			t.Errorf("ExtractVideoID(%q) = %q, %v; want %q", in, got, err, want)
		}
	}

	bad := []string{"", "https://example.com/watch?v=aqz-KE-bpKQ", "https://www.youtube.com/watch?v=short", "https://www.youtube.com/@channel"}
	for _, in := range bad {
		if got, err := ExtractVideoID(in); err == nil {
			t.Errorf("ExtractVideoID(%q) = %q, want error", in, got)
		}
	}
}

func TestExtractPlaylistID(t *testing.T) {
	const want = "PL_Q15fKxrBb4pxTp2zQm6zZU3-h4dlm05"
	ok := []string{
		want,
		"https://www.youtube.com/playlist?list=" + want,
		"youtube.com/playlist?list=" + want,
		"https://m.youtube.com/playlist?list=" + want + "&si=abc",
		"https://music.youtube.com/playlist?list=" + want,
		"https://www.youtube.com/watch?v=aqz-KE-bpKQ&list=" + want + "&index=3",
		"https://youtu.be/aqz-KE-bpKQ?list=" + want,
	}
	for _, in := range ok {
		got, err := ExtractPlaylistID(in)
		if err != nil || got != want {
			t.Errorf("ExtractPlaylistID(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, id := range []string{"UUlzJiwPb3KUnmQEscBtEpRA", "OLAK5uy_kXHJ3pMWMWXL4CVf-6l1G9vV8CvbRQ1Q0"} {
		if got, err := ExtractPlaylistID(id); err != nil || got != id {
			t.Errorf("ExtractPlaylistID(%q) = %q, %v", id, got, err)
		}
	}

	bad := []string{
		"", "aqz-KE-bpKQ", "https://www.youtube.com/watch?v=aqz-KE-bpKQ",
		"https://example.com/playlist?list=" + want,
		"https://www.youtube.com/playlist?list=short",
		"https://www.youtube.com/watch?v=aqz-KE-bpKQ&list=RDaqz-KE-bpKQ",
	}
	for _, in := range bad {
		if got, err := ExtractPlaylistID(in); err == nil {
			t.Errorf("ExtractPlaylistID(%q) = %q, want error", in, got)
		}
	}
}

func TestIsPlaylistPage(t *testing.T) {
	const list = "PL_Q15fKxrBb4pxTp2zQm6zZU3-h4dlm05"
	cases := map[string]bool{
		"https://www.youtube.com/playlist?list=" + list: true,
		list: true,
		"https://music.youtube.com/playlist?list=" + list:          true,
		"https://www.youtube.com/watch?v=aqz-KE-bpKQ&list=" + list: false,
		"https://www.youtube.com/watch?v=aqz-KE-bpKQ":              false,
		"aqz-KE-bpKQ": false,
	}
	for in, want := range cases {
		if got := IsPlaylistPage(in); got != want {
			t.Errorf("IsPlaylistPage(%q) = %v, want %v", in, got, want)
		}
	}
}
