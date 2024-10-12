package downloader_test

import (
	"testing"

	"github.com/Sn0wye/ytd/pkg/downloader"
)

func TestSlugifyFilename(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "Test 🚀 Video_Title... <>",
			expected: "test-video-title",
		},
		{
			input:    "Another Test: Filename@Here!",
			expected: "another-test-filenamehere",
		},
		{
			input:    "file_name_123...",
			expected: "file-name-123",
		},
		{
			input:    "file_with_emoji_😀_and_chars",
			expected: "file-with-emoji-and-chars",
		},
		{
			input:    "   Leading and trailing   ",
			expected: "leading-and-trailing",
		},
		{
			input:    "<>:|*",
			expected: "",
		},
		{
			input:    "   ",
			expected: "",
		},
		{
			input:    "My file name",
			expected: "my-file-name",
		},
		{
			input:    "  some @@file__ with$$$  random  spaces  ",
			expected: "some-file-with-random-spaces",
		},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			result := downloader.SlugifyFilename(test.input)
			if result != test.expected {
				t.Errorf("Expected %q, got %q", test.expected, result)
			}
		})
	}
}
