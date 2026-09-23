package pdflayout

import "pdf2word/internal/pdfiumx"

// ProbeInfo summarises what the extractor can see on one page.
type ProbeInfo struct {
	Width, Height  float64
	Chars          int
	Rules          int
	HRules, VRules int
	Tables         int
	Images         int
}

// Probe reports the raw material available on a page: text characters,
// table rulings, detected tables and images. It is a diagnostic aid.
func Probe(path string, page int) (ProbeInfo, error) {
	d, err := pdfiumx.Open(path)
	if err != nil {
		return ProbeInfo{}, err
	}
	defer d.Close()
	d.Mu.Lock()
	defer d.Mu.Unlock()

	var info ProbeInfo
	w, h, err := pageSize(d, page)
	if err != nil {
		return info, err
	}
	info.Width, info.Height = w, h
	chars, err := readChars(d, page)
	if err != nil {
		return info, err
	}
	info.Chars = len(chars)
	rules, images, _ := readObjects(d, page, w, h, len(chars) > 0)
	info.Rules = len(rules)
	for _, r := range rules {
		if r.vertical {
			info.VRules++
		} else {
			info.HRules++
		}
	}
	info.Tables = len(detectTables(rules))
	info.Images = len(images)
	return info, nil
}
