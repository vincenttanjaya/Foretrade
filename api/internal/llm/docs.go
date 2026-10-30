package llm

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxDocText  = 200_000  // bytes of text per document sent as text
	maxUnzipped = 16 << 20 // bytes inflated from one docx/xlsx: the zip-bomb guard, 10× what maxDocText needs
)

var docMIME = map[string]string{
	"pdf": "application/pdf", "csv": "text/csv", "txt": "text/plain", "md": "text/markdown", "html": "text/html",
	"docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
}

func imageMIME(format string) string { return "image/" + format } // png | jpeg | webp | gif

// fileName is the document's name with its format as the extension.
func fileName(d *Document) string { return d.Name + "." + d.Format }

// ErrUnreadableDoc is a document the provider can't take: a PDF on a wire
// without PDF support, or a docx/xlsx whose text can't be extracted. Retrying
// can't help. The wires return it inside ErrUpstream.
var ErrUnreadableDoc = errors.New("unreadable document")

// docText is a document as text, for wires without native support: text
// formats as they are, docx and xlsx extracted, at most maxDocText bytes.
func docText(d *Document) (string, error) {
	out := &capped{}
	var err error
	switch d.Format {
	case "pdf":
		return "", fmt.Errorf("%w: this provider can't read PDF files", ErrUnreadableDoc)
	case "docx", "xlsx":
		err = officeText(d, out)
	default:
		out.WriteString(string(d.Bytes))
	}
	if err != nil {
		return "", fmt.Errorf("%w: %s could not be read: %w", ErrUnreadableDoc, fileName(d), err)
	}
	if out.cut {
		out.sb.WriteString("\n[truncated]")
	}
	return out.sb.String(), nil
}

// capped collects text up to maxDocText bytes.
type capped struct {
	sb  strings.Builder
	cut bool
}

func (c *capped) WriteString(s string) {
	if n := maxDocText - c.sb.Len(); len(s) > n {
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s, c.cut = s[:max(n, 0)], true
	}
	c.sb.WriteString(s)
}

// office reads parts of an OOXML zip within one inflate budget.
type office struct {
	files map[string]*zip.File
	left  int64
}

func officeText(d *Document, out *capped) error {
	zr, err := zip.NewReader(bytes.NewReader(d.Bytes), int64(len(d.Bytes)))
	if err != nil {
		return errors.New("not a valid " + d.Format + " file")
	}
	x := &office{files: map[string]*zip.File{}, left: maxUnzipped}
	for _, f := range zr.File {
		x.files[f.Name] = f
	}
	if d.Format == "docx" {
		err = x.docx(out)
	} else {
		err = x.xlsx(out)
	}
	if errors.Is(err, errInflated) {
		out.cut = true // the rest would pass the budget: keep what was read
		return nil
	}
	return err
}

var errInflated = errors.New("inflate budget spent")

// decode streams one part through fn's XML decoder; a missing part is an
// error unless optional.
func (x *office) decode(name string, optional bool, fn func(*xml.Decoder) error) error {
	f := x.files[name]
	if f == nil {
		if optional {
			return nil
		}
		return errors.New("missing " + name)
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	lr := &io.LimitedReader{R: rc, N: x.left}
	err = fn(xml.NewDecoder(lr))
	x.left = lr.N
	if err != nil && x.left == 0 {
		return errInflated
	}
	return err
}

// tokens calls fn for each token until EOF or out is full.
func tokens(dec *xml.Decoder, out *capped, fn func(xml.Token)) error {
	for !out.cut {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		fn(tok)
	}
	return nil
}

// docx: the w:t runs of word/document.xml, a newline after each paragraph.
func (x *office) docx(out *capped) error {
	return x.decode("word/document.xml", false, func(dec *xml.Decoder) error {
		inText := false
		return tokens(dec, out, func(tok xml.Token) {
			switch t := tok.(type) {
			case xml.StartElement:
				switch t.Name.Local {
				case "t":
					inText = true
				case "tab":
					out.WriteString("\t")
				case "br", "cr":
					out.WriteString("\n")
				}
			case xml.EndElement:
				switch t.Name.Local {
				case "t":
					inText = false
				case "p":
					out.WriteString("\n")
				}
			case xml.CharData:
				if inText {
					out.WriteString(string(t))
				}
			}
		})
	})
}

// xlsx: each sheet in workbook order as a "## name" heading and tab-separated
// rows, shared strings resolved. Cells show their stored values.
// ponytail: dates stay serial numbers; apply number formats if users need them.
func (x *office) xlsx(out *capped) error {
	shared, err := x.sharedStrings()
	if err != nil {
		return err
	}
	var wb struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			RID  string `xml:"id,attr"` // r:id
		} `xml:"sheets>sheet"`
	}
	var rels struct {
		Rels []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	if err := x.decode("xl/workbook.xml", false, func(d *xml.Decoder) error { return d.Decode(&wb) }); err != nil {
		return err
	}
	if err := x.decode("xl/_rels/workbook.xml.rels", false, func(d *xml.Decoder) error { return d.Decode(&rels) }); err != nil {
		return err
	}
	targets := map[string]string{}
	for _, r := range rels.Rels {
		targets[r.ID] = r.Target
	}
	for _, sh := range wb.Sheets {
		target := targets[sh.RID]
		if target == "" || out.cut {
			continue
		}
		if strings.HasPrefix(target, "/") {
			target = target[1:]
		} else {
			target = path.Join("xl", target)
		}
		out.WriteString("## " + sh.Name + "\n")
		if err := x.decode(target, false, func(d *xml.Decoder) error { return sheetRows(d, shared, out) }); err != nil {
			return err
		}
	}
	return nil
}

// sharedStrings is xl/sharedStrings.xml: each <si>'s text runs, without
// phonetic guides.
func (x *office) sharedStrings() ([]string, error) {
	var list []string
	var cur strings.Builder
	inText, inPhonetic := false, false
	err := x.decode("xl/sharedStrings.xml", true, func(dec *xml.Decoder) error {
		return tokens(dec, &capped{}, func(tok xml.Token) {
			switch t := tok.(type) {
			case xml.StartElement:
				switch t.Name.Local {
				case "si":
					cur.Reset()
				case "t":
					inText = true
				case "rPh":
					inPhonetic = true
				}
			case xml.EndElement:
				switch t.Name.Local {
				case "si":
					list = append(list, cur.String())
				case "t":
					inText = false
				case "rPh":
					inPhonetic = false
				}
			case xml.CharData:
				if inText && !inPhonetic {
					cur.Write(t)
				}
			}
		})
	})
	return list, err
}

// sheetRows writes a worksheet's rows, each cell in the column its reference names.
func sheetRows(dec *xml.Decoder, shared []string, out *capped) error {
	var row []string
	var val strings.Builder
	col, typ, inVal := 0, "", false
	return tokens(dec, out, func(tok xml.Token) {
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				row = row[:0]
			case "c":
				col, typ = len(row), ""
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "r":
						if c, ok := column(a.Value); ok {
							col = c
						}
					case "t":
						typ = a.Value
					}
				}
				val.Reset()
			case "v", "t":
				inVal = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v", "t":
				inVal = false
			case "c":
				v := val.String()
				if typ == "s" {
					if i, err := strconv.Atoi(v); err == nil && i >= 0 && i < len(shared) {
						v = shared[i]
					}
				}
				for len(row) <= col {
					row = append(row, "")
				}
				row[col] = v
			case "row":
				out.WriteString(strings.TrimRight(strings.Join(row, "\t"), "\t") + "\n")
			}
		case xml.CharData:
			if inVal {
				val.Write(t)
			}
		}
	})
}

// column is the 0-based column of a cell reference such as "C7"; false past
// XFD, Excel's last column, checked per letter so n can't overflow.
func column(ref string) (int, bool) {
	n := 0
	for i, r := range ref {
		if r < 'A' || r > 'Z' {
			return n - 1, i > 0
		}
		if n = n*26 + int(r-'A'+1); n > 16384 {
			return 0, false
		}
	}
	return 0, false
}
