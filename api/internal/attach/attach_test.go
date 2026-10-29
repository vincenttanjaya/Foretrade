package attach

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vincenttanjaya/Foretrade/api/internal/attach/attachtest"
)

func input(m map[string]any) Input {
	return Input{Kind: m["kind"].(string), MediaType: m["mediaType"].(string), Name: m["name"].(string), Data: m["data"].(string)}
}

func decodeOne(m map[string]any) error {
	_, err := Decode([]Input{input(m)}, "message")
	return err
}

// Every rejection names the file (or its position) with a specific, safe message.
func TestDecodeRejections(t *testing.T) {
	pdf, csv := attachtest.PDF(1), []byte("a,b\n1,2\n")
	big := append(attachtest.PDF(1), bytes.Repeat([]byte(" "), MaxDocumentBytes)...)
	encoded := attachtest.Doc(pdf, "application/pdf", "r.pdf")
	encoded["data"] = strings.Repeat("A", base64.StdEncoding.EncodedLen(MaxDocumentBytes)+4)
	malformed := attachtest.Doc(pdf, "application/pdf", "r.pdf")
	malformed["data"] = "JVBERi0x\nLjQ="
	small := attachtest.Image(t, "png", 1, 1)
	large := append(slices.Clone(small), make([]byte, MaxImageBytes+1-len(small))...)
	for name, c := range map[string]struct {
		in   map[string]any
		want string
	}{
		"unknown kind":         {map[string]any{"kind": "video", "mediaType": "video/mp4", "name": "v.mp4", "data": "AAAA"}, "kind must be image or document"},
		"legacy Word":          {attachtest.Doc(pdf, "application/msword", "old.doc"), "documents must be PDF, CSV"},
		"image as document":    {attachtest.Doc(small, "image/png", "chart.png"), "documents must be PDF"},
		"empty document":       {attachtest.Doc(nil, "text/plain", "empty.txt"), "empty.txt: file is empty"},
		"document too large":   {attachtest.Doc(big, "application/pdf", "big.pdf"), "big.pdf: file is too large: at most 4500000 bytes"},
		"encoded overflow":     {encoded, "too large"},
		"malformed base64":     {malformed, "standard base64"},
		"not a PDF":            {attachtest.Doc(csv, "application/pdf", "fake.pdf"), "fake.pdf: file content is not a PDF"},
		"docx not a ZIP":       {attachtest.Doc(pdf, attachtest.DOCXType, "memo.docx"), "does not match its Office mediaType"},
		"xlsx sent as docx":    {attachtest.Doc(attachtest.XLSX(t), attachtest.DOCXType, "memo.docx"), "does not match its Office mediaType"},
		"docx sent as xlsx":    {attachtest.Doc(attachtest.DOCX(t), attachtest.XLSXType, "sheet.xlsx"), "does not match its Office mediaType"},
		"ZIP without types":    {attachtest.Doc([]byte("PK\x03\x04junk"), attachtest.XLSXType, "sheet.xlsx"), "does not match its Office mediaType"},
		"invalid UTF-8":        {attachtest.Doc([]byte{'a', 0xff, 'b'}, "text/csv", "latin.csv"), "UTF-8 without NUL"},
		"NUL byte":             {attachtest.Doc([]byte("a\x00b"), "text/plain", "utf16.txt"), "UTF-8 without NUL"},
		"extension mismatch":   {attachtest.Doc(csv, "text/csv", "report.pdf"), "file extension does not match text/csv"},
		"image extension":      {attachtest.Doc(pdf, "application/pdf", "scan.png"), "file extension does not match application/pdf"},
		"document name length": {attachtest.Doc(csv, "text/csv", strings.Repeat("é", 121)), "attachment 1: name must be at most 120 characters"},
		"animated GIF":         {attachtest.Img(attachtest.GIF(2), "image/gif", "anim.gif"), "anim.gif: image encoding header"},
		"image too large":      {attachtest.Img(large, "image/png", "chart.png"), "chart.png: file is too large: at most 1500000 bytes"},
		"image mismatch":       {attachtest.Img(small, "image/jpeg", "chart.jpg"), "image encoding header must match"},
		"unsupported BMP":      {attachtest.Img([]byte("BM"), "image/bmp", "chart.bmp"), "images must be PNG, JPEG, WebP or static GIF"},
		"pixel overflow":       {attachtest.Img(attachtest.Image(t, "png", 4001, 4000), "image/png", "big.png"), "image dimensions"},
	} {
		if err := decodeOne(c.in); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

// The count error names the unit it counts per.
func TestDecodePerNoun(t *testing.T) {
	csv := input(attachtest.Doc([]byte("a\n"), "text/csv", "a.csv"))
	six := []Input{csv, csv, csv, csv, csv, csv}
	for _, per := range []string{"message", "forecast"} {
		if _, err := Decode(slices.Clone(six), per); err == nil || err.Error() != "attach at most 5 files and images per "+per {
			t.Errorf("%s: %v", per, err)
		}
	}
	set, err := Decode(six[:5], "forecast")
	if err != nil || len(set.Files) != 5 || len(set.Documents()) != 5 || set.Files[4].Document.Name != "Document 5 (a)" {
		t.Fatalf("five: %v %+v", err, set.Metadata())
	}
}

// A mixed set keeps request order; documents are numbered among documents only.
func TestDecodeMixedSet(t *testing.T) {
	pdf, photo := attachtest.PDF(3), attachtest.Image(t, "jpeg", 3, 2)
	set, err := Decode([]Input{input(attachtest.Img(photo, "image/jpeg", "IMG_1.HEIC")), input(attachtest.Doc(pdf, "application/pdf", "C:\\x\\Q3 note.pdf")),
		input(attachtest.Doc(attachtest.XLSX(t), attachtest.XLSXType, "book.xlsx"))}, "forecast")
	if err != nil {
		t.Fatal(err)
	}
	m := set.Metadata()
	if len(m) != 3 || m[0] != (Metadata{Kind: "image", MediaType: "image/jpeg", Name: "IMG_1.HEIC", Size: len(photo)}) ||
		m[1] != (Metadata{Kind: "document", MediaType: "application/pdf", Name: "Q3 note.pdf", Size: len(pdf), Pages: 3}) {
		t.Fatalf("metadata %+v", m)
	}
	if set.Files[0].Image == nil || set.Files[0].Document != nil || set.Files[1].Document.Name != "Document 1 (Q3 note)" ||
		set.Files[2].Document.Name != "Document 2 (book)" || len(set.Images()) != 1 || len(set.Documents()) != 2 {
		t.Fatalf("files %+v", set.Files)
	}
	if got := (Set{}).Metadata(); got == nil || len(got) != 0 {
		t.Fatalf("empty metadata %v", got)
	}
}

// Drop releases every byte slice the set holds.
func TestSetDrop(t *testing.T) {
	set, err := Decode([]Input{input(attachtest.Doc(attachtest.PDF(1), "application/pdf", "a.pdf")), input(attachtest.Img(attachtest.GIF(1), "image/gif", "b.gif"))}, "forecast")
	if err != nil {
		t.Fatal(err)
	}
	set.Drop()
	if set.Files[0].Document.Bytes != nil || set.Files[1].Image.Bytes != nil || set.Images()[0].Bytes != nil || set.Documents()[0].Bytes != nil {
		t.Fatal("bytes kept after Drop")
	}
	if set.Files[0].Name != "a.pdf" || set.Files[0].Size == 0 {
		t.Fatal("Drop changed the metadata")
	}
}

// XML allows either quote around attribute values; a different main part or a
// half-quoted value still does not match.
func TestOfficeMainPartQuotes(t *testing.T) {
	main := "application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"
	if err := officeMainPart(attachtest.Office(t, "word/document.xml", main, `'`), main); err != nil {
		t.Fatalf("single quotes: %v", err)
	}
	if err := officeMainPart(attachtest.Office(t, "word/document.xml", "application/vnd.openxmlformats-officedocument.wordprocessingml.template.main+xml", `'`), main); err == nil {
		t.Fatal("template accepted")
	}
	if err := officeMainPart(attachtest.Office(t, "word/document.xml", main+"x", `'`), main); err == nil {
		t.Fatal("longer type accepted")
	}
}

// Upload-sized bodies share one memory budget before they are read: a full
// budget answers ErrBusy while small bodies pass, an undeclared length counts
// as the whole cap, an oversize declared length is refused unread, and every
// release gives its reservation back exactly once.
func TestAdmitBudget(t *testing.T) {
	defer func(w time.Duration) { wait = w }(wait)
	wait = 20 * time.Millisecond
	admit := func(length int64) (func(), error) {
		r := httptest.NewRequest("POST", "/", strings.NewReader(""))
		r.ContentLength = length
		return Admit(httptest.NewRecorder(), r, "u")
	}
	if err := budget.Acquire(t.Context(), 4*BodyLimit); err != nil {
		t.Fatal(err)
	}
	if _, err := admit(SmallBody + 1); !errors.Is(err, ErrBusy) {
		t.Fatalf("full budget: %v", err)
	}
	if release, err := admit(SmallBody); err != nil {
		t.Fatalf("small body while full: %v", err)
	} else {
		release()
	}
	budget.Release(BodyLimit - 1) // one byte short of a cap-sized share
	if _, err := admit(-1); !errors.Is(err, ErrBusy) {
		t.Fatalf("undeclared length counts as the cap: %v", err)
	}
	budget.Release(3*BodyLimit + 1)
	if _, err := admit(BodyLimit + 1); !errors.Is(err, ErrTooLarge) || err.Error() != "request body is too large" {
		t.Fatalf("oversize declared length: %v", err)
	}
	release, err := admit(BodyLimit)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := Saturate(time.Millisecond); ok {
		t.Fatal("Saturate succeeded while a body holds its share")
	}
	release()
	release() // idempotent
	free, ok := Saturate(time.Millisecond)
	if !ok || wait != time.Millisecond {
		t.Fatal("budget leaked")
	}
	if _, err := admit(SmallBody + 1); !errors.Is(err, ErrBusy) {
		t.Fatalf("saturated: %v", err)
	}
	free()
	free()
	if wait != 20*time.Millisecond || !budget.TryAcquire(4*BodyLimit) {
		t.Fatal("Saturate did not give the budget back")
	}
	budget.Release(4 * BodyLimit)
}

// Millions of tiny attachment objects fit the body cap; decoding stops after
// the first rejected one instead of allocating gigabytes of structs.
func TestListBounded(t *testing.T) {
	raw := []byte(`{"message":"Read these","attachments":[` + strings.TrimSuffix(strings.Repeat("{},", 2_000_000), ",") + `]}`)
	var req struct {
		Attachments List `json:"attachments"`
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if len(req.Attachments) != Max+1 || after.TotalAlloc-before.TotalAlloc > 64<<20 {
		t.Fatalf("decoded %d items, allocated %d bytes", len(req.Attachments), after.TotalAlloc-before.TotalAlloc)
	}
	if _, err := Decode(req.Attachments, "forecast"); err == nil || !strings.Contains(err.Error(), "at most 5 files") {
		t.Fatalf("many items: %v", err)
	}
	var typ *json.UnmarshalTypeError
	if err := json.Unmarshal([]byte(`{"attachments":5}`), &req); !errors.As(err, &typ) || typ.Field != "attachments" {
		t.Fatalf("not an array: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"attachments":[{"kind":5}]}`), &req); !errors.As(err, &typ) || typ.Field != "attachments.kind" {
		t.Fatalf("wrong item type: %v", err)
	}
	for _, body := range []string{`null`, `[]`} {
		req.Attachments = nil
		if err := json.Unmarshal([]byte(`{"attachments":`+body+`}`), &req); err != nil || len(req.Attachments) != 0 {
			t.Fatalf("%s: %v %v", body, req.Attachments, err)
		}
	}
}

// Only the decoded bytes outlive decoding: the request's base64 is dropped,
// valid or not, so a long run does not also hold the encoded copy.
func TestDecodeDropsBase64(t *testing.T) {
	pdf := attachtest.PDF(2)
	inputs := []Input{input(attachtest.Doc(pdf, "application/pdf", "a.pdf")), input(attachtest.Img(attachtest.Image(t, "png", 2, 2), "image/png", "b.png"))}
	set, err := Decode(inputs, "message")
	if err != nil || len(set.Documents()) != 1 || !bytes.Equal(set.Documents()[0].Bytes, pdf) || len(set.Images()) != 1 {
		t.Fatalf("decode: %v", err)
	}
	bad := []Input{input(attachtest.Doc(pdf, "application/pdf", "a.pdf")), input(attachtest.Doc(pdf, "text/csv", "c.pdf"))}
	if _, err := Decode(bad, "message"); err == nil {
		t.Fatal("mismatch accepted")
	}
	for _, in := range append(inputs, bad...) {
		if in.Data != "" {
			t.Fatalf("%s kept its base64", in.Name)
		}
	}
}

func TestDocumentName(t *testing.T) {
	for in, want := range map[string]string{
		"report.pdf":               "Document 1 (report)",
		"Q3  report (final)!!.PDF": "Document 1 (Q3 report final)",
		"..pdf":                    "Document 1",
		"日本語.csv":                  "Document 1",
		"a--b_c[d].txt":            "Document 1 (a--b c d)",
		"\t]] [[ .md":              "Document 1",
		strings.Repeat("ab ", 30):  "Document 1 (ab ab ab ab ab ab ab ab ab ab ab ab ab)",
	} {
		if got := DocumentName(in, 1); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
	if DocumentName("a.csv", 2) == DocumentName("a.txt", 1) {
		t.Fatal("names not unique")
	}
}

func TestPDFPages(t *testing.T) {
	for name, c := range map[string]struct {
		data []byte
		want int
	}{
		"root count":       {attachtest.PDF(12), 12},
		"largest node":     {append(attachtest.PDF(12), "4 0 obj << /Type /Pages /Parent 2 0 R /Count 4 >> endobj"...), 12},
		"compressed":       {[]byte("%PDF-1.7\n1 0 obj << /Type /ObjStm /N 3 >> stream\nx\x9c\x00endstream"), 0},
		"unterminated":     {[]byte("%PDF-1.4 << /Type /Pages /Count 3"), 0},
		"no dictionary":    {[]byte("%PDF-1.4 /Type /Pages /Count 3 >>"), 0},
		"Page leaves only": {[]byte("%PDF-1.4 << /Type /Page /Count 9 >>"), 0},
	} {
		if got := pdfPages(c.data); got != c.want {
			t.Errorf("%s: got %d want %d", name, got, c.want)
		}
	}
}

func TestGIFDimensions(t *testing.T) {
	if w, h, err := gifDimensions(attachtest.GIF(1)); err != nil || w != 2 || h != 1 {
		t.Fatalf("static: %d×%d %v", w, h, err)
	}
	local := []byte("GIF87a\x01\x00\x01\x00\x00\x00\x00\x2c\x00\x00\x00\x00\x01\x00\x01\x00\x80\x00\x00\x00\xff\xff\xff\x02\x02\x44\x01\x00\x3b")
	if _, _, err := gifDimensions(local); err != nil {
		t.Fatalf("local colour table: %v", err)
	}
	static := attachtest.GIF(1)
	invalid := map[string][]byte{
		"animated":         attachtest.GIF(2),
		"no frame":         attachtest.GIF(0),
		"no trailer":       static[:len(static)-1],
		"truncated frame":  static[:len(static)-4],
		"bad signature":    append([]byte("GIF90a"), static[6:]...),
		"short header":     static[:12],
		"unknown block":    append(slices.Clone(static[:len(static)-1]), 0x99, 0x3b),
		"zero width":       append([]byte("GIF89a\x00\x00"), static[8:]...),
		"truncated colour": static[:15],
	}
	for name, data := range invalid {
		if _, _, err := gifDimensions(data); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := decodeOne(attachtest.Img(static, "image/gif", "chart.gif")); err != nil {
		t.Fatalf("static GIF rejected: %v", err)
	}
}

func riffChunk(kind string, data []byte) []byte {
	b := make([]byte, 8)
	copy(b, kind)
	binary.LittleEndian.PutUint32(b[4:], uint32(len(data)))
	b = append(b, data...)
	if len(data)%2 != 0 {
		b = append(b, 0)
	}
	return b
}
func riffWebp(chunks ...[]byte) []byte {
	b := []byte("RIFF\x00\x00\x00\x00WEBP")
	for _, chunk := range chunks {
		b = append(b, chunk...)
	}
	binary.LittleEndian.PutUint32(b[4:8], uint32(len(b)-8))
	return b
}

func TestImageHeaderFormats(t *testing.T) {
	webp := attachtest.WebP(t)
	vp8 := slices.Clone(webp[20:])
	// VP8L header fixture isolates lossless dimension/version parsing, without a raster allocation.
	lossless := riffWebp(riffChunk("VP8L", []byte{0x2f, 0, 0, 0, 0}))
	extended := riffWebp(riffChunk("VP8X", make([]byte, 10)), riffChunk("VP8 ", vp8))
	for name, c := range map[string]struct {
		data  []byte
		media string
	}{
		"png side boundary":    {attachtest.Image(t, "png", 8000, 1), "image/png"},
		"png pixel boundary":   {attachtest.Image(t, "png", 4000, 4000), "image/png"},
		"jpeg":                 {attachtest.Image(t, "jpeg", 1, 2), "image/jpeg"},
		"webp lossy":           {webp, "image/webp"},
		"webp lossless header": {lossless, "image/webp"},
		"webp extended header": {extended, "image/webp"},
	} {
		if err := decodeOne(attachtest.Img(c.data, c.media, "../\x00.png")); err != nil {
			t.Errorf("%s rejected: %v", name, err)
		}
	}
	invalid := map[string][]byte{"truncated RIFF": webp[:11], "truncated payload": webp[:len(webp)-1], "missing payload": riffWebp(riffChunk("VP8X", make([]byte, 10))), "duplicate image": riffWebp(riffChunk("VP8 ", vp8), riffChunk("VP8 ", vp8))}
	bad := slices.Clone(webp)
	bad[0] = 'X'
	invalid["wrong signature"] = bad
	bad = slices.Clone(webp)
	bad[23] = 0
	invalid["wrong VP8 signature"] = bad
	bad = slices.Clone(webp)
	binary.LittleEndian.PutUint32(bad[16:20], 0xffffffff)
	invalid["chunk overflow"] = bad
	bad = slices.Clone(extended)
	bad[24] = 1
	invalid["canvas mismatch"] = bad
	bad = slices.Clone(extended)
	bad[20] = 2
	invalid["animated flag"] = bad
	bad = slices.Clone(lossless)
	bad[24] = 0xe0
	invalid["lossless version"] = bad
	bad = slices.Clone(lossless)
	bad[len(bad)-1] = 1
	invalid["invalid padding"] = bad
	zero := slices.Clone(vp8)
	zero[6], zero[7] = 0, 0
	invalid["zero primary then second image"] = riffWebp(riffChunk("VP8 ", zero), riffChunk("VP8 ", vp8))
	for name, data := range invalid {
		if err := decodeOne(attachtest.Img(data, "image/webp", "chart.webp")); err == nil {
			t.Errorf("%s: malformed WebP accepted", name)
		}
	}
}
