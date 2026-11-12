package agent

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
)

// SaveImage keeps an image a tool returned (base64, of type mime) as a file
// under <stateDir>/images, named by its bytes, rather than inside the
// journal: what an image part's "path" names (server chatimage.go). It
// answers the absolute path and the size, and false for an image that does
// not decode or cannot be written.
func SaveImage(stateDir, mime, b64 string) (string, int, bool) {
	if stateDir == "" {
		return "", 0, false
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(data) == 0 {
		return "", 0, false
	}
	sum := sha256.Sum256(data)
	ext := ".png"
	switch mime {
	case "image/jpeg":
		ext = ".jpg"
	case "image/gif":
		ext = ".gif"
	case "image/webp":
		ext = ".webp"
	}
	dir, err := filepath.Abs(filepath.Join(stateDir, "images"))
	if err != nil {
		return "", 0, false
	}
	path := filepath.Join(dir, hex.EncodeToString(sum[:12])+ext)
	if err := os.MkdirAll(dir, 0o700); err != nil || os.WriteFile(path, data, 0o600) != nil {
		return "", 0, false
	}
	return path, len(data), true
}
