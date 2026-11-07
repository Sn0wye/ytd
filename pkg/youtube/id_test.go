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
