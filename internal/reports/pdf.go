package reports

import (
	"fmt"
	"strings"
)

// BuildPDF renders the evidence as a minimal, dependency-free PDF:
// text lines on A4 pages using the built-in Helvetica fonts. It is a
// readable export of the same content as the HTML pack, not a
// pixel-faithful replica.
func BuildPDF(e *Evidence) []byte {
	var lines []pdfLineInput
	lines = append(lines, pdfLineInput{text: e.Title, heading: 2})
	lines = append(lines, pdfLineInput{text: "Generated " + e.GeneratedAt.Format("2006-01-02 15:04:05 UTC") + " by adcp-test v0.1"})
	lines = append(lines, pdfLineInput{text: ""})
	for _, s := range e.Sections {
		lines = append(lines, pdfLineInput{text: s.Title, heading: 1})
		if s.Summary != "" {
			lines = append(lines, pdfLineInput{text: s.Summary})
		}
		if len(s.Headers) > 0 {
			lines = append(lines, pdfLineInput{text: strings.Join(s.Headers, " | ")})
			lines = append(lines, pdfLineInput{text: strings.Repeat("-", 100)})
			for _, row := range s.Rows {
				lines = append(lines, pdfLineInput{text: "  " + strings.Join(row, " | ")})
			}
		} else {
			for _, l := range s.Lines {
				lines = append(lines, pdfLineInput{text: "  - " + l})
			}
		}
		lines = append(lines, pdfLineInput{text: ""})
	}

	// Paginate and wrap.
	const (
		pageWidth  = 595.0
		marginLeft = 50.0
		topY       = 780.0
		bottomY    = 50.0
		bodySize   = 10.0
		lead       = 14.0
		titleSize  = 16.0
		sectSize   = 13.0
		wrapAt     = 105
	)
	var pages [][]pdfLineInput
	cur := []pdfLineInput{}
	y := topY
	flush := func() {
		if len(cur) > 0 {
			pages = append(pages, cur)
		}
		cur = []pdfLineInput{}
		y = topY
	}
	emit := func(l pdfLineInput, size float64) {
		for _, w := range wrap(l.text, wrapAt) {
			if y-size < bottomY {
				flush()
			}
			cur = append(cur, pdfLineInput{text: w, heading: l.heading})
			y -= lead
		}
	}
	for _, l := range lines {
		switch l.heading {
		case 2:
			if y < topY { // title always starts a page
				flush()
			}
			emit(l, titleSize)
			y -= 6
		case 1:
			if y < topY-60 {
				flush()
			}
			emit(l, sectSize)
			y -= 2
		default:
			emit(l, bodySize)
		}
	}
	flush()
	_ = pageWidth

	return assemblePDF(pages, marginLeft, topY, lead)
}

// wrap breaks s into chunks of at most n runes, preferring spaces.
func wrap(s string, n int) []string {
	if s == "" {
		return []string{""}
	}
	var out []string
	rs := []rune(s)
	for len(rs) > 0 {
		if len(rs) <= n {
			out = append(out, string(rs))
			break
		}
		cut := n
		for i := n; i > n-20 && i > 0; i-- {
			if rs[i] == ' ' {
				cut = i
				break
			}
		}
		out = append(out, string(rs[:cut]))
		rs = rs[cut:]
		for len(rs) > 0 && rs[0] == ' ' {
			rs = rs[1:]
		}
	}
	return out
}

// pdfEscape makes s safe for a PDF literal string (WinAnsi-ish: only
// printable ASCII survives).
func pdfEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '(':
			b.WriteString(`\(`)
		case r == ')':
			b.WriteString(`\)`)
		case r >= 32 && r < 127:
			b.WriteRune(r)
		case r == '\t':
			b.WriteString("    ")
		default:
			b.WriteRune('?')
		}
	}
	return b.String()
}

// assemblePDF builds the PDF bytes from paginated lines.
func assemblePDF(pages [][]pdfLineInput, marginLeft, topY, lead float64) []byte {
	// Lay out absolute positions.
	type laid struct {
		text string
		x, y float64
		size float64
		bold bool
	}
	var laidPages [][]laid
	for _, pg := range pages {
		y := topY
		var lp []laid
		for _, l := range pg {
			size := 10.0
			bold := false
			switch l.heading {
			case 2:
				size = 16
				bold = true
			case 1:
				size = 13
				bold = true
			}
			lp = append(lp, laid{text: l.text, x: marginLeft, y: y, size: size, bold: bold})
			y -= lead
			if l.heading == 2 {
				y -= 6
			} else if l.heading == 1 {
				y -= 2
			}
		}
		laidPages = append(laidPages, lp)
	}

	var buf strings.Builder
	buf.WriteString("%PDF-1.4\n")
	offsets := []int{}
	addObj := func(body string) int {
		offsets = append(offsets, buf.Len())
		id := len(offsets)
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", id, body)
		return id
	}

	// Reserve ids: 1=catalog, 2=pages, 3=helvetica, 4=helvetica-bold,
	// then per page: content stream + page object.
	nPages := len(laidPages)
	pageObjIDs := make([]int, nPages)
	// We add in order; compute ids arithmetically instead.
	catalogID := addObj("<< /Type /Catalog /Pages 2 0 R >>")
	_ = catalogID
	kids := strings.Builder{}
	nextID := 5 // after catalog(1) pages(2) fonts(3,4)
	for i := range laidPages {
		contentID := nextID
		pageID := nextID + 1
		nextID += 2
		pageObjIDs[i] = pageID
		fmt.Fprintf(&kids, "%d 0 R ", pageID)
		_ = contentID
	}
	pagesID := addObj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids.String(), nPages))
	_ = pagesID
	fontID := addObj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	boldID := addObj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold >>")
	_ = fontID
	_ = boldID

	for i, lp := range laidPages {
		var cs strings.Builder
		for _, l := range lp {
			font := "/F1"
			if l.bold {
				font = "/F2"
			}
			fmt.Fprintf(&cs, "BT %s %.1f Tf 1 0 0 1 %.1f %.1f Tm (%s) Tj ET\n",
				font, l.size, l.x, l.y, pdfEscape(l.text))
		}
		content := cs.String()
		addObj(fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content))
		// page object; parent is object 2
		addObj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 3 0 R /F2 4 0 R >> >> /Contents %d 0 R >>",
			pageObjIDs[i]-1))
	}

	xrefAt := buf.Len()
	n := len(offsets) + 1
	fmt.Fprintf(&buf, "xref\n0 %d\n", n)
	buf.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", n, xrefAt)
	return []byte(buf.String())
}

// pdfLineInput is one wrapped line awaiting layout.
type pdfLineInput struct {
	text    string
	heading int
}
