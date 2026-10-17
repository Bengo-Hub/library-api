package handlers

import (
	"bytes"
	"strings"

	"github.com/Bengo-Hub/httpware/objectstore"
	"github.com/disintegration/imaging"
)

// Library files on the shared object storage (httpware/objectstore):
//   - covers: public, library/<tenant>/covers/<bib id>/<uuid>.<ext> plus <uuid>_thumb.<ext> (the
//     UI derives the thumbnail link by inserting "_thumb" before the extension). Bib records keep
//     the public link, the same as covers from Open Library.
//   - ebooks: private, library/<tenant>/ebooks/<yyyy>/<mm>/<uuid>.<ext>; the e-book keeps the key
//     and the API streams it to readers with an active loan.

const (
	maxCoverBytes   = 8 << 20
	coverThumbWidth = 320
)

// RegisterMediaTypes sets the library's media rules on the shared registry. Call once at startup.
func RegisterMediaTypes() {
	objectstore.Register(objectstore.MediaType{
		Name: objectstore.TypeCovers, Public: true, MaxBytes: maxCoverBytes, Image: true,
		Allowed: []string{"image/jpeg", "image/png", "image/webp", "image/gif"},
	})
	objectstore.Register(objectstore.MediaType{
		Name: objectstore.TypeEbooks, MaxBytes: 100 << 20,
		// An EPUB is sniffed as application/epub+zip; any other zip is not readable and refused.
		Allowed: []string{"application/pdf", "application/epub+zip", "audio/mpeg"},
	})
}

// coverThumbnail is a proportionally resized copy (coverThumbWidth px wide) in the source format.
// WebP and GIF have no encoder here; the caller treats a failure as "no thumbnail".
func coverThumbnail(data []byte, ext string) ([]byte, error) {
	format, err := imaging.FormatFromExtension(ext)
	if err != nil {
		return nil, err
	}
	src, err := imaging.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := imaging.Encode(&buf, imaging.Resize(src, coverThumbWidth, 0, imaging.Lanczos), format); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// thumbRef is the thumbnail of a stored cover (same folder, "_thumb" before the extension).
func thumbRef(cover string) string {
	i := strings.LastIndex(cover, ".")
	if i <= strings.LastIndex(cover, "/") {
		return ""
	}
	return cover[:i] + "_thumb" + cover[i:]
}
