package fileutil

import (
	"net/http"
	"os"
	"strings"
)

// IsBinary reports whether the file at path appears to be a binary file.
// It reads the first 512 bytes and uses http.DetectContentType to check
// if the MIME type starts with "text/". Files that cannot be read or are
// empty are considered non-binary (to avoid false positives on new/temp files).
func IsBinary(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	if n == 0 {
		return false
	}

	mime := http.DetectContentType(buf[:n])
	return !strings.HasPrefix(mime, "text/")
}