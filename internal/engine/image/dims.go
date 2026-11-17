package image

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// encodedDims reads the pixel dimensions of an AVIF or WebP file the engine
// just wrote, without a vipsheader process. Dimensions are still read back
// from the encoded bytes, never assumed from the raster that was encoded.
func encodedDims(path, format string) (int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	// Both containers keep the dimensions near the start: the WebP chunk
	// header within 30 bytes, the AVIF `ispe` property inside `meta`, which
	// libheif writes before `mdat`.
	head := make([]byte, 4096)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return 0, 0, err
	}
	head = head[:n]
	switch format {
	case FormatWebP:
		return webpDims(head)
	case FormatAVIF:
		return avifDims(head)
	}
	return 0, 0, fmt.Errorf("encodedDims: unsupported format %q", format)
}

func webpDims(b []byte) (int, int, error) {
	if len(b) < 30 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WEBP" {
		return 0, 0, errors.New("webp: bad RIFF header")
	}
	switch string(b[12:16]) {
	case "VP8X": // extended: 24-bit canvas width-1 and height-1
		w := int(b[24]) | int(b[25])<<8 | int(b[26])<<16
		h := int(b[27]) | int(b[28])<<8 | int(b[29])<<16
		return w + 1, h + 1, nil
	case "VP8 ": // lossy: frame tag (3), start code 9d 01 2a, 14-bit dims
		if b[23] != 0x9d || b[24] != 0x01 || b[25] != 0x2a {
			return 0, 0, errors.New("webp: bad VP8 start code")
		}
		w := int(binary.LittleEndian.Uint16(b[26:28]) & 0x3fff)
		h := int(binary.LittleEndian.Uint16(b[28:30]) & 0x3fff)
		return w, h, nil
	case "VP8L": // lossless: signature 0x2f, then 14-bit width-1, height-1
		if b[20] != 0x2f {
			return 0, 0, errors.New("webp: bad VP8L signature")
		}
		v := binary.LittleEndian.Uint32(b[21:25])
		return int(v&0x3fff) + 1, int((v>>14)&0x3fff) + 1, nil
	}
	return 0, 0, errors.New("webp: unknown chunk " + string(b[12:16]))
}

// avifDims returns the image spatial extent (`ispe`) of the first item.
// The engine writes single-image AVIFs without grids or rotation, so the
// first `ispe` is the primary image.
func avifDims(b []byte) (int, int, error) {
	i := bytes.Index(b, []byte("ispe"))
	// box: size(4) "ispe" version/flags(4) width(4) height(4)
	if i < 4 || i+16 > len(b) {
		return 0, 0, errors.New("avif: no ispe property")
	}
	w := int(binary.BigEndian.Uint32(b[i+8 : i+12]))
	h := int(binary.BigEndian.Uint32(b[i+12 : i+16]))
	if w <= 0 || h <= 0 {
		return 0, 0, errors.New("avif: invalid ispe")
	}
	return w, h, nil
}
