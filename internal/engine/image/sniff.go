package image

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
)

// Formats recognised by the sniffer. The names match SourceInfo.Format and
// preset.ImageLimits.Accept.
const (
	FormatJPEG = "jpeg"
	FormatPNG  = "png"
	FormatGIF  = "gif"
	FormatWebP = "webp"
	FormatHEIC = "heic"
	FormatAVIF = "avif"
	FormatTIFF = "tiff"
)

var contentTypes = map[string]string{
	FormatJPEG: "image/jpeg",
	FormatPNG:  "image/png",
	FormatGIF:  "image/gif",
	FormatWebP: "image/webp",
	FormatHEIC: "image/heic",
	FormatAVIF: "image/avif",
	FormatTIFF: "image/tiff",
}

// expectedLoader is the libvips loader that must handle each format. Probe
// rejects a file when vipsheader picked a different loader, so a file can
// never reach a loader we did not intend to expose.
var expectedLoader = map[string]string{
	FormatJPEG: "jpegload",
	FormatPNG:  "pngload",
	FormatGIF:  "gifload",
	FormatWebP: "webpload",
	FormatHEIC: "heifload",
	FormatAVIF: "heifload",
	FormatTIFF: "tiffload",
}

// sniffLen is how many leading bytes the sniffer reads. It is enough for an
// ISO-BMFF ftyp box with a reasonable list of compatible brands.
const sniffLen = 512

// sniffFormat detects the image format from magic bytes. It never looks at
// the file name or a declared content type. It returns "" when the format is
// not recognised.
func sniffFormat(head []byte) string {
	switch {
	case len(head) >= 3 && head[0] == 0xFF && head[1] == 0xD8 && head[2] == 0xFF:
		return FormatJPEG
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")):
		return FormatPNG
	case bytes.HasPrefix(head, []byte("GIF87a")), bytes.HasPrefix(head, []byte("GIF89a")):
		return FormatGIF
	case len(head) >= 12 && bytes.Equal(head[0:4], []byte("RIFF")) && bytes.Equal(head[8:12], []byte("WEBP")):
		return FormatWebP
	case bytes.HasPrefix(head, []byte("II*\x00")), bytes.HasPrefix(head, []byte("MM\x00*")):
		return FormatTIFF
	case len(head) >= 12 && bytes.Equal(head[4:8], []byte("ftyp")):
		return sniffFtyp(head)
	}
	return ""
}

// sniffFtyp classifies an ISO-BMFF file by its ftyp box: the major brand
// first, then the compatible brands.
func sniffFtyp(head []byte) string {
	size := int(binary.BigEndian.Uint32(head[0:4]))
	if size < 16 || size > len(head) {
		size = len(head)
	}
	major := string(head[8:12])
	var compat []string
	for off := 16; off+4 <= size; off += 4 {
		compat = append(compat, string(head[off:off+4]))
	}
	if f := brandFormat(major); f != "" {
		return f
	}
	// Generic HEIF brands (mif1, msf1, miaf) say nothing about the codec;
	// decide by the compatible brands, preferring AVIF.
	var sawHEIF bool
	for _, b := range compat {
		switch brandFormat(b) {
		case FormatAVIF:
			return FormatAVIF
		case FormatHEIC:
			sawHEIF = true
		}
	}
	if sawHEIF || major == "mif1" || major == "msf1" {
		return FormatHEIC
	}
	return ""
}

func brandFormat(brand string) string {
	switch brand {
	case "avif", "avis":
		return FormatAVIF
	case "heic", "heix", "heim", "heis", "hevc", "hevx", "hevm", "hevs":
		return FormatHEIC
	}
	return ""
}

// errTruncated reports a structurally incomplete file.
var errTruncated = errors.New("file is truncated or structurally invalid")

// checkStructure performs a cheap linear scan (no pixel decoding) that
// detects truncated JPEG and PNG files. libvips decodes such files with
// gray fill by default, so they would otherwise produce broken renditions.
func checkStructure(path, format string) error {
	switch format {
	case FormatJPEG, FormatPNG:
	default:
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	if format == FormatJPEG {
		return checkJPEG(r)
	}
	return checkPNG(r)
}

// checkJPEG walks the marker segments (so embedded EXIF thumbnails are
// skipped by length) and the entropy-coded data until it finds EOI.
func checkJPEG(r *bufio.Reader) error {
	var soi [2]byte
	if _, err := io.ReadFull(r, soi[:]); err != nil || soi[0] != 0xFF || soi[1] != 0xD8 {
		return errTruncated
	}
	var pending byte // marker already consumed by the entropy scan, 0 if none
	for {
		m := pending
		pending = 0
		if m == 0 {
			b, err := r.ReadByte()
			if err != nil || b != 0xFF {
				return errTruncated
			}
			for b == 0xFF { // fill bytes
				if b, err = r.ReadByte(); err != nil {
					return errTruncated
				}
			}
			m = b
		}
		switch {
		case m == 0xD9: // EOI
			return nil
		case m == 0x01 || (m >= 0xD0 && m <= 0xD7): // standalone markers
			continue
		}
		var lb [2]byte
		if _, err := io.ReadFull(r, lb[:]); err != nil {
			return errTruncated
		}
		n := int(binary.BigEndian.Uint16(lb[:]))
		if n < 2 {
			return errTruncated
		}
		if _, err := r.Discard(n - 2); err != nil {
			return errTruncated
		}
		if m != 0xDA { // not SOS
			continue
		}
		// Entropy-coded data: 0xFF00 is a stuffed byte, 0xFFD0..D7 are
		// restart markers; any other 0xFFxx ends the scan.
		for pending == 0 {
			b, err := r.ReadByte()
			if err != nil {
				return errTruncated
			}
			if b != 0xFF {
				continue
			}
			for b == 0xFF {
				if b, err = r.ReadByte(); err != nil {
					return errTruncated
				}
			}
			if b == 0x00 || (b >= 0xD0 && b <= 0xD7) {
				continue
			}
			pending = b
		}
	}
}

// checkPNG walks the chunk list until IEND.
func checkPNG(r *bufio.Reader) error {
	if _, err := r.Discard(8); err != nil {
		return errTruncated
	}
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return errTruncated
		}
		n := int64(binary.BigEndian.Uint32(hdr[0:4]))
		if string(hdr[4:8]) == "IEND" {
			return nil
		}
		// data + CRC
		if _, err := io.CopyN(io.Discard, r, n+4); err != nil {
			return errTruncated
		}
	}
}
