// Package attachtest builds small synthetic attachment files for tests: a
// PDF with a page tree, OOXML packages, GIFs, PNG/JPEG images and a WebP, and
// the JSON inputs a client sends for them. Nothing here is a real document.
package attachtest

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"testing"
)

const (
	DOCXType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	XLSXType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
)

// PDF is a minimal synthetic PDF whose page-tree root declares pages.
func PDF(pages int) []byte {
	return fmt.Appendf(nil, "%%PDF-1.4\n1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj\n"+
		"2 0 obj << /Kids [3 0 R] /Type /Pages /Count %d >> endobj\n3 0 obj << /Type /Page /Parent 2 0 R >> endobj\ntrailer << /Root 1 0 R >>\n%%%%EOF\n", pages)
}

// Office is a synthetic OOXML package declaring mainType as its main part,
// its content-types attributes in quote q.
func Office(t testing.TB, part, mainType, q string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, body := range map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Override PartName=` + q + `/` + part + q + ` ContentType=` + q + mainType + q + `/></Types>`,
		part: "<root/>",
	} {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func DOCX(t testing.TB) []byte {
	return Office(t, "word/document.xml", "application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml", `"`)
}

func XLSX(t testing.TB) []byte {
	return Office(t, "xl/workbook.xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml", `"`)
}

// GIF is a synthetic 2×1 GIF with the given number of frames.
func GIF(frames int) []byte {
	b := []byte("GIF89a\x02\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff")
	b = append(b, "\x21\xff\x0bNETSCAPE2.0\x03\x01\x00\x00\x00"...) // a loop extension
	for range frames {
		b = append(b, "\x21\xf9\x04\x00\x00\x00\x00\x00"...)
		b = append(b, "\x2c\x00\x00\x00\x00\x02\x00\x01\x00\x00\x02\x02\x44\x01\x00"...)
	}
	return append(b, 0x3b)
}

// Image is a grey PNG or JPEG ("jpeg") of the given size.
func Image(t testing.TB, format string, width, height int) []byte {
	t.Helper()
	var b bytes.Buffer
	var err error
	if format == "jpeg" {
		err = jpeg.Encode(&b, image.NewGray(image.Rect(0, 0, width, height)), nil)
	} else {
		err = png.Encode(&b, image.NewGray(image.Rect(0, 0, width, height)))
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// WebP is a complete one-pixel VP8 WebP (it also decodes with an independent codec).
func WebP(t testing.TB) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString("UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Doc is the JSON input for a document.
func Doc(data []byte, media, name string) map[string]any {
	return map[string]any{"kind": "document", "mediaType": media, "name": name, "data": base64.StdEncoding.EncodeToString(data)}
}

// Img is the JSON input for an image.
func Img(data []byte, media, name string) map[string]any {
	return map[string]any{"kind": "image", "mediaType": media, "name": name, "data": base64.StdEncoding.EncodeToString(data)}
}
