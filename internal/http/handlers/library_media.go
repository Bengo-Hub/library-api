package handlers

import (
	"github.com/Bengo-Hub/httpware/objectstore"
)

// Library files on the shared object storage (httpware/objectstore):
//   - covers: public, library/<tenant>/covers/<bib id>/<uuid>.<ext>. Bib records keep the public
//     link, the same as covers from Open Library. Smaller sizes for grids come from Cloudflare
//     Image Transformations at the edge, so no thumbnail copy is stored.
//   - ebooks: private, library/<tenant>/ebooks/<yyyy>/<mm>/<uuid>.<ext>; the e-book keeps the key
//     and the API streams it to readers with an active loan.

const maxCoverBytes = 8 << 20

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
