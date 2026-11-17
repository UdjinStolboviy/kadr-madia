package pipeline

import "testing"

func TestKindFromHint(t *testing.T) {
	for hint, want := range map[string]string{
		"image/jpeg photo.jpg":                "image",
		"video/mp4 ":                          "video",
		"application/octet-stream clip.MOV":   "video",
		"https://x.com/a/b.png?sig=1 ":        "image",
		"https://example.com/photo ":          "",
		"application/octet-stream  noext":     "",
		"application/octet-stream  archive.z": "",
	} {
		if got := kindFromHint(hint); got != want {
			t.Errorf("%q: got %q, want %q", hint, got, want)
		}
	}
}
