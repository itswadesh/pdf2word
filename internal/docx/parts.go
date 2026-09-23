package docx

import (
	"fmt"
	"sort"
	"strings"
)

// Static OOXML package parts. Only the main document part and the parts
// that list images are generated dynamically.

func contentTypesXML(imageExts []string) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
`)
	for _, ext := range imageExts {
		fmt.Fprintf(&sb, "  <Default Extension=%q ContentType=%q/>\n", ext, imageContentType(ext))
	}
	sb.WriteString(`  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
  <Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>
  <Override PartName="/word/settings.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.settings+xml"/>
  <Override PartName="/word/fontTable.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.fontTable+xml"/>
  <Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>
  <Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>
</Types>
`)
	return sb.String()
}

func imageContentType(ext string) string {
	switch ext {
	case "jpg", "jpeg":
		return "image/jpeg"
	default:
		return "image/png"
	}
}

const rootRelsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>
  <Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/>
</Relationships>
`

// documentRelsXML lists the styles, settings and font table parts and one
// relationship per image.
func documentRelsXML(images []imagePart) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/settings" Target="settings.xml"/>
  <Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/fontTable" Target="fontTable.xml"/>
`)
	for _, im := range images {
		fmt.Fprintf(&sb, "  <Relationship Id=%q Type=\"http://schemas.openxmlformats.org/officeDocument/2006/relationships/image\" Target=\"media/%s\"/>\n", im.rid, im.name)
	}
	sb.WriteString("</Relationships>\n")
	return sb.String()
}

// settingsXML asks for current Word layout rules (compatibility mode 15,
// Word 2013 and later). Among other things, a justified line that ends
// with a line break is then stretched to the margin like any other, which
// pages that keep the PDF's line breaks rely on.
const settingsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:settings xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:compat>
    <w:compatSetting w:name="compatibilityMode" w:uri="http://schemas.microsoft.com/office/word" w:val="15"/>
  </w:compat>
</w:settings>
`

// fontTableXML declares every font the document uses with the standard
// font to substitute when it is not installed (w:altName), so a machine
// without the PDF's typeface still gets one of similar width.
func fontTableXML(fonts map[string]string) string {
	names := make([]string, 0, len(fonts))
	for name := range fonts {
		names = append(names, name)
	}
	sort.Strings(names)
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:fonts xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
`)
	for _, name := range names {
		fallback := fonts[name]
		family, pitch := "auto", "variable"
		switch fallback {
		case "Times New Roman":
			family = "roman"
		case "Arial":
			family = "swiss"
		case "Courier New":
			family, pitch = "modern", "fixed"
		}
		fmt.Fprintf(&sb, "  <w:font w:name=%q>", escapeAttr(name))
		if fallback != "" && fallback != name {
			fmt.Fprintf(&sb, `<w:altName w:val=%q/>`, escapeAttr(fallback))
		}
		fmt.Fprintf(&sb, `<w:family w:val=%q/><w:pitch w:val=%q/></w:font>`+"\n", family, pitch)
	}
	sb.WriteString("</w:fonts>\n")
	return sb.String()
}

// DefaultComplexScriptFont is asked for complex scripts (Odia, Hindi,
// Bengali, Arabic, ...). Word ignores the Latin font for those runs; without
// a font that has the glyphs the text shows as boxes. Nirmala UI ships with
// Windows and covers all Indic scripts.
const DefaultComplexScriptFont = "Nirmala UI"

// stylesXMLTemplate defines Normal, Heading1 and Heading2 with sensible
// defaults (Calibri 11pt body, bold 16pt / 13pt headings). %s is the
// complex-script font.
const stylesXMLTemplate = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:docDefaults>
    <w:rPrDefault>
      <w:rPr>
        <w:rFonts w:ascii="Calibri" w:hAnsi="Calibri" w:eastAsia="Calibri" w:cs="%s"/>
        <w:sz w:val="22"/>
        <w:szCs w:val="22"/>
        <w:kern w:val="2"/>
        <w:lang w:val="en-US" w:bidi="or-IN"/>
      </w:rPr>
    </w:rPrDefault>
    <w:pPrDefault>
      <w:pPr>
        <w:widowControl w:val="0"/>
        <w:spacing w:after="160" w:line="259" w:lineRule="auto"/>
      </w:pPr>
    </w:pPrDefault>
  </w:docDefaults>
  <w:style w:type="paragraph" w:default="1" w:styleId="Normal">
    <w:name w:val="Normal"/>
    <w:qFormat/>
  </w:style>
  <w:style w:type="paragraph" w:styleId="Heading1">
    <w:name w:val="heading 1"/>
    <w:basedOn w:val="Normal"/>
    <w:next w:val="Normal"/>
    <w:qFormat/>
    <w:pPr>
      <w:spacing w:before="240" w:after="80"/>
      <w:outlineLvl w:val="0"/>
    </w:pPr>
    <w:rPr>
      <w:sz w:val="32"/>
      <w:szCs w:val="32"/>
    </w:rPr>
  </w:style>
  <w:style w:type="paragraph" w:styleId="Heading2">
    <w:name w:val="heading 2"/>
    <w:basedOn w:val="Normal"/>
    <w:next w:val="Normal"/>
    <w:qFormat/>
    <w:pPr>
      <w:spacing w:before="200" w:after="60"/>
      <w:outlineLvl w:val="1"/>
    </w:pPr>
    <w:rPr>
      <w:sz w:val="26"/>
      <w:szCs w:val="26"/>
    </w:rPr>
  </w:style>
  <w:style w:type="table" w:default="1" w:styleId="TableNormal">
    <w:name w:val="Normal Table"/>
    <w:tblPr>
      <w:tblInd w:w="0" w:type="dxa"/>
      <w:tblCellMar>
        <w:top w:w="0" w:type="dxa"/>
        <w:left w:w="108" w:type="dxa"/>
        <w:bottom w:w="0" w:type="dxa"/>
        <w:right w:w="108" w:type="dxa"/>
      </w:tblCellMar>
    </w:tblPr>
  </w:style>
</w:styles>
`

const appXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties" xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes">
  <Application>pdf2word</Application>
</Properties>
`

// coreXMLTemplate takes the creation timestamp (W3CDTF / RFC 3339 UTC) twice.
const coreXMLTemplate = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:dcmitype="http://purl.org/dc/dcmitype/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <dc:creator>pdf2word</dc:creator>
  <cp:lastModifiedBy>pdf2word</cp:lastModifiedBy>
  <dcterms:created xsi:type="dcterms:W3CDTF">%s</dcterms:created>
  <dcterms:modified xsi:type="dcterms:W3CDTF">%s</dcterms:modified>
</cp:coreProperties>
`
