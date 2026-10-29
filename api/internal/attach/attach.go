// Package attach validates the files and images a user attaches to a chat
// message or a forecast: bounded base64, header sniffing, mediaType and
// extension agreement, and the process-wide budget for upload-sized bodies.
// It never parses a document beyond its headers; bytes live only in memory.
package attach

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"path"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sync/semaphore"

	"github.com/vincenttanjaya/Foretrade/api/internal/llm"
)

const (
	// BodyLimit fits five maximum documents after base64 (5 × 6,000,000 bytes)
	// plus a fully escaped 4000-rune message (or a forecast draft) and names,
	// with room for JSON keys.
	BodyLimit        = 33_554_432
	Max              = 5 // files and images per message / per forecast
	MaxImageBytes    = 1_500_000
	MaxDocumentBytes = 4_500_000 // the Converse per-document limit
	// SmallBody: a body of at most this size (text and old two-image
	// messages, text-only forecasts) skips the shared budget.
	SmallBody = 4 << 20
)

// budget bounds the upload-sized bodies read and decoded at once, chat and
// forecasts together, in declared bytes: decoding allocates about five times
// the body (the JSON buffer, base64 strings and decoded bytes), and the API
// host has 2 GB. A body with no declared length counts as the whole cap.
var budget = semaphore.NewWeighted(4 * BodyLimit)

var wait = 30 * time.Second // then ErrBusy (429): a full budget frees as uploads finish

// perUser is how many upload-sized bodies one user may have admitted at once
// (gap round G23): a third waits like a full budget, then gets ErrBusy, so one
// account cannot hold the whole budget.
const perUser = 2

// An admitted upload-sized body must arrive by readGrace plus its declared
// length at readRate, at most readMax (gap round G23): a body trickled a byte
// a second would otherwise hold its share of the budget indefinitely.
var (
	readGrace = 20 * time.Second
	readRate  = int64(128 << 10) // bytes a second
	readMax   = 5 * time.Minute
)

// users holds each user's upload slots while anyone holds or waits for one.
var users = struct {
	sync.Mutex
	m map[string]*userSlots
}{m: map[string]*userSlots{}}

type userSlots struct {
	sem  *semaphore.Weighted
	refs int
}

// slotsOf is user's slots and the func that lets them go.
func slotsOf(user string) (*semaphore.Weighted, func()) {
	users.Lock()
	defer users.Unlock()
	u := users.m[user]
	if u == nil {
		u = &userSlots{sem: semaphore.NewWeighted(perUser)}
		users.m[user] = u
	}
	u.refs++
	return u.sem, func() {
		users.Lock()
		defer users.Unlock()
		if u.refs--; u.refs == 0 {
			delete(users.m, user)
		}
	}
}

var (
	ErrTooLarge = errors.New("request body is too large")
	ErrBusy     = errors.New("too many files are uploading right now; try again in a moment")
)

// Admit reserves the body's share of the budget before it is read, one of
// user's perUser upload slots, and sets a read deadline for the body on w's
// connection (readGrace + its length at readRate, at most readMax). The
// release frees both and clears the deadline; it is idempotent: the caller
// releases right after decoding and defers it.
func Admit(w http.ResponseWriter, r *http.Request, user string) (release func(), err error) {
	n := r.ContentLength
	if n >= 0 && n <= SmallBody {
		return func() {}, nil
	}
	if n > BodyLimit {
		return nil, ErrTooLarge
	}
	if n < 0 {
		n = BodyLimit
	}
	ctx, cancel := context.WithTimeout(r.Context(), wait)
	defer cancel()
	slots, done := slotsOf(user)
	if err := slots.Acquire(ctx, 1); err != nil {
		done()
		return nil, ErrBusy
	}
	if err := budget.Acquire(ctx, n); err != nil {
		slots.Release(1)
		done()
		return nil, ErrBusy
	}
	rc := http.NewResponseController(w)
	setDeadline(rc, time.Now().Add(min(readGrace+time.Duration(n/readRate)*time.Second, readMax)))
	return sync.OnceFunc(func() {
		setDeadline(rc, time.Time{})
		budget.Release(n)
		slots.Release(1)
		done()
	}), nil
}

// setDeadline sets the connection's read deadline. Its error is ignored: a
// writer that cannot (a test recorder: http.ErrNotSupported) keeps none, and
// a connection that is gone fails its body read on its own.
func setDeadline(rc *http.ResponseController, at time.Time) { _ = rc.SetReadDeadline(at) }

// Saturate holds the whole budget and shortens Admit's wait to d until
// release runs, so handler tests can check their 429 mapping without
// uploading 128 MiB. ok is false (and nothing is held) when some admitted
// body has not been released: tests use it to prove nothing leaked.
func Saturate(d time.Duration) (release func(), ok bool) {
	if !budget.TryAcquire(4 * BodyLimit) {
		return func() {}, false
	}
	prev := wait
	wait = d
	return sync.OnceFunc(func() { wait = prev; budget.Release(4 * BodyLimit) }), true
}

// Input is one attachment as the client sends it (contract AttachmentInput).
type Input struct {
	Kind      string `json:"kind"`
	MediaType string `json:"mediaType"`
	Name      string `json:"name"`
	Data      string `json:"data"`
}

// List stops decoding after Max+1 items, which Decode then rejects: within
// the body cap, millions of tiny objects would otherwise allocate gigabytes
// of empty structs first.
type List []Input

func (l *List) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*l = nil
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('[') {
		return &json.UnmarshalTypeError{Value: "value", Type: reflect.TypeFor[List](), Field: "attachments"}
	}
	out := List{}
	for len(out) <= Max && dec.More() {
		var in Input
		if err := dec.Decode(&in); err != nil {
			if typ, ok := err.(*json.UnmarshalTypeError); ok { // the same field path a plain slice reports
				typ.Field = strings.TrimSuffix("attachments."+typ.Field, ".")
			}
			return err
		}
		out = append(out, in)
	}
	*l = out
	return nil
}

// Metadata is an attachment's safe description, stored and returned: never bytes.
type Metadata struct {
	Kind      string `json:"kind"`
	MediaType string `json:"mediaType"`
	Name      string `json:"name"`
	Size      int    `json:"size"`
	Pages     int    `json:"pages,omitempty"` // PDFs, when the page tree is readable
}

// File is one validated attachment; exactly one of Image and Document is set.
type File struct {
	Metadata
	Image    *llm.Image
	Document *llm.Document // Name = DocumentName(name, n), n = 1.. over the set's documents
}

// Set is one request's validated attachments, in request order; bytes belong only to this run.
type Set struct{ Files []File }

func (s Set) Images() []llm.Image {
	var out []llm.Image
	for _, f := range s.Files {
		if f.Image != nil {
			out = append(out, *f.Image)
		}
	}
	return out
}

func (s Set) Documents() []llm.Document {
	var out []llm.Document
	for _, f := range s.Files {
		if f.Document != nil {
			out = append(out, *f.Document)
		}
	}
	return out
}

// Metadata is every file's metadata, in order; never nil.
func (s Set) Metadata() []Metadata {
	out := make([]Metadata, len(s.Files))
	for i, f := range s.Files {
		out[i] = f.Metadata
	}
	return out
}

// Drop releases every file's bytes: callers drop them as soon as they are sent.
func (s *Set) Drop() {
	for i := range s.Files {
		s.Files[i].Drop()
	}
}

// Drop releases the file's bytes.
func (f *File) Drop() {
	if f.Image != nil {
		f.Image.Bytes = nil
	}
	if f.Document != nil {
		f.Document.Bytes = nil
	}
}

var imageFormats = map[string]string{"image/png": "png", "image/jpeg": "jpeg", "image/webp": "webp", "image/gif": "gif"}

var documentFormats = map[string]string{
	"application/pdf": "pdf", "text/csv": "csv", "text/plain": "txt", "text/markdown": "md", "text/html": "html",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document": "docx",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":       "xlsx",
}

// extensionFormats maps a file-name extension to its format. A document whose
// name carries one of these must agree with its mediaType (text formats agree
// with each other); images are re-encoded by the browser, so their names may not.
var extensionFormats = map[string]string{
	".pdf": "pdf", ".docx": "docx", ".xlsx": "xlsx", ".csv": "csv", ".txt": "txt", ".md": "md", ".markdown": "md",
	".html": "html", ".htm": "html", ".png": "png", ".jpg": "jpeg", ".jpeg": "jpeg", ".webp": "webp", ".gif": "gif",
}

func textFormat(f string) bool { return f == "csv" || f == "txt" || f == "md" || f == "html" }

// Decode checks every allocation bound before decoding and inspects headers
// only, never allocating a pixel buffer or inflating an archive member beyond
// its small content-types part. per names the unit in the count error
// ("message" or "forecast"). Errors are safe 400 messages naming the file.
// The inputs' base64 is dropped on return: the request outlives the run.
func Decode(inputs []Input, per string) (Set, error) {
	defer func() {
		for i := range inputs {
			inputs[i].Data = ""
		}
	}()
	var out Set
	if len(inputs) > Max {
		return out, fmt.Errorf("attach at most %d files and images per %s", Max, per)
	}
	documents := 0
	for i, in := range inputs {
		name, err := safeName(in.Name, in.Kind)
		if err != nil {
			return Set{}, fmt.Errorf("attachment %d: %v", i+1, err)
		}
		var f File
		switch in.Kind {
		case "image":
			var img llm.Image
			img, f.Metadata, err = decodeImage(in, name)
			f.Image = &img
		case "document":
			documents++
			var doc llm.Document
			doc, f.Metadata, err = decodeDocument(in, name, documents)
			f.Document = &doc
		default:
			err = errors.New("attachment kind must be image or document")
		}
		if err != nil {
			return Set{}, fmt.Errorf("%s: %v", name, err)
		}
		out.Files = append(out.Files, f)
	}
	return out, nil
}

// safeName keeps the basename without control characters, for display and history only.
func safeName(raw, kind string) (string, error) {
	name := path.Base(strings.ReplaceAll(raw, "\\", "/"))
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name))
	if name == "" || name == "." || name == ".." || name == "/" {
		name = "image"
		if kind == "document" {
			name = "document"
		}
	}
	if utf8.RuneCountInString(name) > 120 {
		return "", errors.New("name must be at most 120 characters")
	}
	return name, nil
}

// decodeBase64 bounds the encoded length before allocating, then decodes strictly.
func decodeBase64(data string, limit int) ([]byte, error) {
	bad := fmt.Errorf("data must be standard base64 of 1..%d decoded bytes", limit)
	if len(data) == 0 {
		return nil, errors.New("file is empty")
	}
	if len(data) > base64.StdEncoding.EncodedLen(limit) {
		return nil, fmt.Errorf("file is too large: at most %d bytes", limit)
	}
	// DecodeString permits CR/LF; reject those too so encoded length is a strict bound.
	if strings.ContainsAny(data, "\r\n") {
		return nil, bad
	}
	b, err := base64.StdEncoding.Strict().DecodeString(data)
	if err != nil || len(b) == 0 {
		return nil, bad
	}
	if len(b) > limit {
		return nil, fmt.Errorf("file is too large: at most %d bytes", limit)
	}
	return b, nil
}

func decodeImage(in Input, name string) (llm.Image, Metadata, error) {
	format, ok := imageFormats[in.MediaType]
	if !ok {
		return llm.Image{}, Metadata{}, errors.New("images must be PNG, JPEG, WebP or static GIF")
	}
	data, err := decodeBase64(in.Data, MaxImageBytes)
	if err != nil {
		return llm.Image{}, Metadata{}, err
	}
	var width, height int
	switch format {
	case "png":
		cfg, e := png.DecodeConfig(bytes.NewReader(data))
		width, height, err = cfg.Width, cfg.Height, e
	case "jpeg":
		cfg, e := jpeg.DecodeConfig(bytes.NewReader(data))
		width, height, err = cfg.Width, cfg.Height, e
	case "webp":
		width, height, err = webpDimensions(data)
	case "gif":
		width, height, err = gifDimensions(data)
	}
	if err != nil {
		return llm.Image{}, Metadata{}, errors.New("image encoding header must match its mediaType")
	}
	if width < 1 || height < 1 || width > 8000 || height > 8000 || int64(width)*int64(height) > 16_000_000 {
		return llm.Image{}, Metadata{}, errors.New("image dimensions must be at most 8000 per side and 16000000 pixels")
	}
	return llm.Image{Format: format, Bytes: data}, Metadata{Kind: "image", MediaType: in.MediaType, Name: name, Size: len(data)}, nil
}

// decodeDocument sniffs the content against its mediaType. n numbers the
// message's documents for the model-facing name.
func decodeDocument(in Input, name string, n int) (llm.Document, Metadata, error) {
	format, ok := documentFormats[in.MediaType]
	if !ok {
		return llm.Document{}, Metadata{}, errors.New("documents must be PDF, CSV, TXT, Markdown, HTML, DOCX or XLSX")
	}
	if f, ok := extensionFormats[strings.ToLower(path.Ext(name))]; ok && f != format && !(textFormat(f) && textFormat(format)) {
		return llm.Document{}, Metadata{}, fmt.Errorf("file extension does not match %s", in.MediaType)
	}
	data, err := decodeBase64(in.Data, MaxDocumentBytes)
	if err != nil {
		return llm.Document{}, Metadata{}, err
	}
	m := Metadata{Kind: "document", MediaType: in.MediaType, Name: name, Size: len(data)}
	switch format {
	case "pdf":
		if !bytes.HasPrefix(data, []byte("%PDF-")) {
			return llm.Document{}, Metadata{}, errors.New("file content is not a PDF")
		}
		m.Pages = pdfPages(data)
	case "docx":
		err = officeMainPart(data, "application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml")
	case "xlsx":
		err = officeMainPart(data, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml")
	default:
		if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			err = errors.New("text files must be UTF-8 without NUL bytes")
		}
	}
	if err != nil {
		return llm.Document{}, Metadata{}, err
	}
	return llm.Document{Format: format, Name: DocumentName(name, n), Bytes: data}, m, nil
}

// DocumentName derives the model-facing name: Converse allows only
// alphanumerics, single spaces, hyphens, parentheses and square brackets, and
// names must be unique in the message. The number makes it unique; a short
// sanitized stem keeps it recognizable without carrying the user's text.
func DocumentName(name string, n int) string {
	stem := strings.TrimSuffix(name, path.Ext(name))
	stem = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r < utf8.RuneSelf && (r == '-' || unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return r
		}
		return ' '
	}, stem)), " ")
	if len(stem) > 40 { // whole words where possible
		cut := stem[:40]
		if i := strings.LastIndexByte(cut, ' '); stem[40] != ' ' && i > 0 {
			cut = cut[:i]
		}
		stem = strings.TrimSpace(cut)
	}
	out := "Document " + strconv.Itoa(n)
	if stem != "" {
		out += " (" + stem + ")"
	}
	return out
}

// officeMainPart checks an OOXML package: a ZIP whose [Content_Types].xml
// declares the expected main part (macro-enabled and template variants differ).
func officeMainPart(data []byte, contentType string) error {
	bad := errors.New("file content does not match its Office mediaType")
	if !bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		return bad
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return bad
	}
	for _, f := range zr.File {
		if f.Name != "[Content_Types].xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return bad
		}
		types, err := io.ReadAll(io.LimitReader(rc, 1<<20))
		rc.Close()
		if err != nil || !bytes.Contains(types, []byte(`"`+contentType+`"`)) && !bytes.Contains(types, []byte(`'`+contentType+`'`)) {
			return bad // XML allows either quote
		}
		return nil
	}
	return bad
}

var (
	pdfPagesNode = regexp.MustCompile(`/Type\s*/Pages\b`)
	pdfCount     = regexp.MustCompile(`/Count\s+(\d{1,6})\b`)
)

// pdfPages reads the page count from an uncompressed page-tree root (the
// largest /Count of a /Type /Pages dictionary); 0 when it is not readable,
// e.g. inside a compressed object stream.
func pdfPages(data []byte) int {
	pages := 0
	for _, loc := range pdfPagesNode.FindAllIndex(data, 1000) {
		lo, hi := max(loc[0]-1024, 0), min(loc[1]+1024, len(data)) // a dictionary, not the whole file
		start := bytes.LastIndex(data[lo:loc[0]], []byte("<<"))
		end := bytes.Index(data[loc[1]:hi], []byte(">>"))
		if start < 0 || end < 0 {
			continue
		}
		if m := pdfCount.FindSubmatch(data[lo+start : loc[1]+end]); m != nil {
			if n, _ := strconv.Atoi(string(m[1])); n > pages {
				pages = n
			}
		}
	}
	return pages
}

// gifDimensions walks the GIF block structure and accepts exactly one frame:
// Converse reads only static images. No frame is decoded.
func gifDimensions(data []byte) (int, int, error) {
	bad := errors.New("invalid static GIF")
	if len(data) < 13 || (string(data[:6]) != "GIF87a" && string(data[:6]) != "GIF89a") {
		return 0, 0, bad
	}
	width, height := int(binary.LittleEndian.Uint16(data[6:8])), int(binary.LittleEndian.Uint16(data[8:10]))
	pos := 13
	if data[10]&0x80 != 0 {
		pos += 3 << (1 + data[10]&7)
	}
	subBlocks := func() bool { // skips data sub-blocks through the terminator
		for pos < len(data) {
			n := int(data[pos])
			pos += 1 + n
			if n == 0 {
				return pos <= len(data)
			}
		}
		return false
	}
	frames := 0
	for pos < len(data) {
		b := data[pos]
		pos++
		switch b {
		case 0x21: // extension: label, then sub-blocks
			pos++
			if !subBlocks() {
				return 0, 0, bad
			}
		case 0x2c: // image descriptor, optional local colour table, LZW code size, sub-blocks
			if frames++; frames > 1 || len(data)-pos < 10 {
				return 0, 0, bad
			}
			packed := data[pos+8]
			pos += 9
			if packed&0x80 != 0 {
				pos += 3 << (1 + packed&7)
			}
			pos++
			if !subBlocks() {
				return 0, 0, bad
			}
		case 0x3b:
			if frames != 1 || width < 1 || height < 1 {
				return 0, 0, bad
			}
			return width, height, nil
		default:
			return 0, 0, bad
		}
	}
	return 0, 0, bad
}

// webpDimensions validates the RIFF/chunk bounds and the static VP8/VP8L image
// header. Extended canvas dimensions must agree with the actual image header.
// A header reader avoids a new codec dependency and full raster allocation.
func webpDimensions(data []byte) (int, int, error) {
	bad := errors.New("invalid static WebP header")
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" || uint64(binary.LittleEndian.Uint32(data[4:8]))+8 != uint64(len(data)) {
		return 0, 0, bad
	}
	width, height, cw, ch := 0, 0, 0, 0
	extended := false
	for pos := 12; pos < len(data); {
		if len(data)-pos < 8 {
			return 0, 0, bad
		}
		kind := string(data[pos : pos+4])
		size := uint64(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))
		pos += 8
		if size > uint64(len(data)-pos) {
			return 0, 0, bad
		}
		chunk := data[pos : pos+int(size)]
		pos += int(size)
		if size&1 != 0 {
			if pos >= len(data) || data[pos] != 0 {
				return 0, 0, bad
			}
			pos++
		}
		switch kind {
		case "VP8X":
			if extended || width != 0 || len(chunk) != 10 || chunk[0]&0xc3 != 0 || chunk[1] != 0 || chunk[2] != 0 || chunk[3] != 0 {
				return 0, 0, bad
			}
			extended = true
			cw = 1 + int(chunk[4]) + (int(chunk[5]) << 8) + (int(chunk[6]) << 16)
			ch = 1 + int(chunk[7]) + (int(chunk[8]) << 8) + (int(chunk[9]) << 16)
		case "VP8 ":
			if width != 0 || len(chunk) < 10 || chunk[0]&1 != 0 || !bytes.Equal(chunk[3:6], []byte{0x9d, 0x01, 0x2a}) {
				return 0, 0, bad
			}
			width = int(binary.LittleEndian.Uint16(chunk[6:8]) & 0x3fff)
			height = int(binary.LittleEndian.Uint16(chunk[8:10]) & 0x3fff)
			if width == 0 || height == 0 {
				return 0, 0, bad
			}
		case "VP8L":
			if width != 0 || len(chunk) < 5 || chunk[0] != 0x2f || chunk[4]&0xe0 != 0 {
				return 0, 0, bad
			}
			bits := binary.LittleEndian.Uint32(chunk[1:5])
			width = int(bits&0x3fff) + 1
			height = int((bits>>14)&0x3fff) + 1
		case "ANIM", "ANMF":
			return 0, 0, bad
		}
	}
	if width < 1 || height < 1 || extended && (cw != width || ch != height) {
		return 0, 0, bad
	}
	return width, height, nil
}
