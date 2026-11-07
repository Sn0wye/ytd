package utils

import (
	"regexp"
	"strings"
)

var (
	invalidChars = regexp.MustCompile(`[:/<>\:"\\|?*!@$.,']`)
	symbols      = regexp.MustCompile(`[\p{So}\p{Cf}]`)
	spaces       = regexp.MustCompile(`\s+`)
	hyphens      = regexp.MustCompile(`-+`)
)

// SlugifyFilename turns an arbitrary title into a safe, lowercase, hyphenated file name.
func SlugifyFilename(fileName string) string {
	fileName = invalidChars.ReplaceAllString(fileName, "")
	fileName = symbols.ReplaceAllString(fileName, "")
	fileName = spaces.ReplaceAllString(fileName, "-")
	fileName = strings.ReplaceAll(fileName, "_", "-")
	fileName = hyphens.ReplaceAllString(fileName, "-")
	fileName = strings.ToLower(fileName)
	return strings.Trim(fileName, "-")
}
