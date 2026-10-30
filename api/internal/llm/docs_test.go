package llm

import (
	"archive/zip"
	"bytes"
	"errors"
	"html"
	"io"
	"strings"
	"testing"
	"time"
)

func zipOf(t testing.TB, files ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f[0])
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, f[1])
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const wordNS = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`

// docxFile is a minimal Word file with one run per paragraph.
func docxFile(t testing.TB, paragraphs ...string) []byte {
	var body strings.Builder
	for _, p := range paragraphs {
		body.WriteString(`<w:p><w:r><w:t xml:space="preserve">` + html.EscapeString(p) + `</w:t></w:r></w:p>`)
	}
	return zipOf(t, [2]string{"word/document.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document ` + wordNS +
		`><w:body>` + body.String() + `</w:body></w:document>`})
}

const (
	sheetNS = `xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"`
	relsNS  = `xmlns="http://schemas.openxmlformats.org/package/2006/relationships"`
)

// xlsxFile is a minimal workbook: sheet "Prices" with a header row and one quote.
func xlsxFile(t testing.TB) []byte {
	return zipOf(t,
		[2]string{"xl/workbook.xml", `<workbook ` + sheetNS + `><sheets><sheet name="Prices" sheetId="1" r:id="rId1"/></sheets></workbook>`},
		[2]string{"xl/_rels/workbook.xml.rels", `<Relationships ` + relsNS + `><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`},
		[2]string{"xl/sharedStrings.xml", `<sst ` + sheetNS + `><si><t>Symbol</t></si><si><t>Price</t></si><si><t>BTC</t></si></sst>`},
		[2]string{"xl/worksheets/sheet1.xml", `<worksheet ` + sheetNS + `><sheetData>` +
			`<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>` +
			`<row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2"><v>100000</v></c></row></sheetData></worksheet>`},
	)
}

func TestDocxText(t *testing.T) {
	doc := zipOf(t, [2]string{"word/document.xml", `<w:document ` + wordNS + `><w:body>` +
		`<w:p><w:r><w:t>Q3 </w:t></w:r><w:r><w:t>revenue</w:t><w:tab/><w:t>up 4%</w:t></w:r></w:p>` +
		`<w:p><w:r><w:delText>deleted</w:delText><w:instrText>PAGE</w:instrText><w:t>Line</w:t><w:br/><w:t>break &amp; more</w:t></w:r></w:p>` +
		`<w:tbl><w:tr><w:tc><w:p><w:r><w:t>cell</w:t></w:r></w:p></w:tc></w:tr></w:tbl></w:body></w:document>`})
	got, err := docText(&Document{Format: "docx", Name: "Document 1", Bytes: doc})
	if want := "Q3 revenue\tup 4%\nLine\nbreak & more\ncell\n"; err != nil || got != want {
		t.Fatalf("%q %v, want %q", got, err, want)
	}
}

// Sheets come in workbook order through the relationships, each cell in its
// referenced column; shared, inline, phonetic and boolean cells resolve.
// An out-of-range reference falls back to the next column.
func TestXlsxText(t *testing.T) {
	book := zipOf(t,
		[2]string{"xl/workbook.xml", `<workbook ` + sheetNS + `><sheets><sheet name="Summary" sheetId="2" r:id="rId9"/><sheet name="Data" sheetId="1" r:id="rId1"/></sheets></workbook>`},
		[2]string{"xl/_rels/workbook.xml.rels", `<Relationships ` + relsNS + `><Relationship Id="rId1" Target="worksheets/sheet1.xml"/>` +
			`<Relationship Id="rId9" Target="/xl/worksheets/sheet2.xml"/></Relationships>`},
		[2]string{"xl/sharedStrings.xml", `<sst ` + sheetNS + `><si><t>東京</t><rPh><t>トウキョウ</t></rPh></si><si><r><t>Rich </t></r><r><t>text</t></r></si></sst>`},
		[2]string{"xl/worksheets/sheet1.xml", `<worksheet ` + sheetNS + `><sheetData>` +
			`<row r="1"><c r="A1" t="s"><v>0</v></c><c r="C1"><v>1.5</v></c></row>` +
			`<row r="3"><c r="B3" t="inlineStr"><is><t>inline</t></is></c><c r="D3" t="b"><v>1</v></c><c r="E3" t="s"><v>99</v></c></row></sheetData></worksheet>`},
		[2]string{"xl/worksheets/sheet2.xml", `<worksheet ` + sheetNS + `><sheetData><row><c t="s"><v>1</v></c><c><f>SUM(A1)</f><v>3</v></c>` +
			`<c r="ZZZZZZZZZZZZZZ1"><v>7</v></c></row></sheetData></worksheet>`}, // a reference past column XFD (or int) goes in the next column
	)
	got, err := docText(&Document{Format: "xlsx", Name: "Document 1", Bytes: book})
	if want := "## Summary\nRich text\t3\t7\n## Data\n東京\t\t1.5\n\tinline\t\t1\t99\n"; err != nil || got != want {
		t.Fatalf("%q %v\nwant %q", got, err, want)
	}
}

// A zip bomb inflates no further than the budget and still yields what came
// before it; text past maxDocText is cut on a rune boundary and marked.
func TestDocTextLimits(t *testing.T) {
	bomb := zipOf(t, [2]string{"word/document.xml", `<w:document ` + wordNS + `><w:body><w:p><w:r><w:t>kept</w:t></w:r></w:p><w:sectPr>` +
		strings.Repeat("a", maxUnzipped+(8<<20)) + `</w:sectPr></w:body></w:document>`})
	if len(bomb) > 1<<20 {
		t.Fatalf("bomb is %d bytes compressed", len(bomb))
	}
	start := time.Now()
	got, err := docText(&Document{Format: "docx", Name: "Bomb", Bytes: bomb})
	if err != nil || got != "kept\n\n[truncated]" {
		t.Fatalf("bomb: %q %v", got, err)
	}
	t.Logf("bomb stopped after %v", time.Since(start))

	long, err := docText(&Document{Format: "txt", Name: "Long", Bytes: []byte("a" + strings.Repeat("é", maxDocText))})
	if err != nil || !strings.HasSuffix(long, "é\n[truncated]") || len(long) > maxDocText+len("\n[truncated]") {
		t.Fatalf("long text: %d bytes, %q", len(long), long[len(long)-20:])
	}

	for name, d := range map[string]*Document{
		"not a zip":    {Format: "docx", Name: "Junk", Bytes: []byte("PK not really")},
		"missing part": {Format: "docx", Name: "Empty", Bytes: zipOf(t, [2]string{"other.xml", "<x/>"})},
		"no workbook":  {Format: "xlsx", Name: "Empty", Bytes: zipOf(t, [2]string{"xl/sharedStrings.xml", "<sst/>"})},
		"pdf":          {Format: "pdf", Name: "Scan", Bytes: []byte("%PDF")},
	} {
		if _, err := docText(d); !errors.Is(err, ErrUnreadableDoc) {
			t.Errorf("%s: %v, want ErrUnreadableDoc", name, err)
		}
	}
	if s, err := docText(&Document{Format: "xlsx", Name: "Prices", Bytes: xlsxFile(t)}); err != nil || s != "## Prices\nSymbol\tPrice\nBTC\t100000\n" {
		t.Fatalf("minimal workbook: %q %v", s, err)
	}
}
