package pdflayout

import (
	"math"
	"sort"

	"pdf2word/internal/model"
)

// rule is a horizontal or vertical straight line segment (table ruling).
type rule struct {
	vertical bool
	pos      float64 // x for vertical, y for horizontal
	from, to float64 // extent along the other axis
	color    string  // RRGGBB
}

const (
	ruleTol           = 1.5  // pt: rules this close are the same grid line
	ruleMinLength     = 6.0  // pt: shorter strokes are not rulings
	ruleMinFillLength = 15.0 // pt: thin filled shapes shorter than this are glyphs
	ruleMaxThick      = 2.0  // pt: thicker filled shapes are not rulings
)

// gridLine is a run of touching collinear rules.
type gridLine struct {
	pos      float64
	from, to float64
	color    string
}

func (g gridLine) covers(from, to float64) bool {
	overlap := math.Min(g.to, to) - math.Max(g.from, from)
	return overlap >= 0.5*(to-from)
}

// cluster merges rules at (almost) the same position whose extents touch or
// overlap into grid lines, sorted by position then extent.
func cluster(rules []rule) []gridLine {
	sort.Slice(rules, func(i, j int) bool {
		if math.Abs(rules[i].pos-rules[j].pos) > ruleTol {
			return rules[i].pos < rules[j].pos
		}
		return rules[i].from < rules[j].from
	})
	var out []gridLine
	for _, r := range rules {
		if n := len(out); n > 0 && math.Abs(out[n-1].pos-r.pos) <= ruleTol && r.from <= out[n-1].to+2*ruleTol {
			out[n-1].to = math.Max(out[n-1].to, r.to)
			out[n-1].pos = (out[n-1].pos + r.pos) / 2
			continue
		}
		out = append(out, gridLine{pos: r.pos, from: r.from, to: r.to, color: r.color})
	}
	return out
}

// cellSlot is one (possibly merged) cell of a table row.
type cellSlot struct {
	col, span int
	lines     []textLine
}

// table is a detected lattice with text assigned to cells.
type table struct {
	x0, y0, x1, y1 float64
	cols           []float64 // column boundaries, ascending x
	rows           []float64 // row boundaries, descending y (top first)
	vlines         []gridLine
	color          string
	cells          [][]cellSlot // per row
}

func (t table) contains(x, y float64) bool {
	return x >= t.x0-ruleTol && x <= t.x1+ruleTol && y >= t.y0-ruleTol && y <= t.y1+ruleTol
}

// detectTables finds lattices of rulings: connected groups with at least two
// vertical and two horizontal grid lines.
func detectTables(rules []rule) []table {
	var vs, hs []rule
	for _, r := range rules {
		if r.to-r.from < ruleMinLength {
			continue
		}
		if r.vertical {
			vs = append(vs, r)
		} else {
			hs = append(hs, r)
		}
	}
	vLines := cluster(vs)
	hLines := cluster(hs)
	if len(vLines) < 2 || len(hLines) < 2 {
		return nil
	}

	// Union-find over grid lines connected by intersections.
	n := len(vLines) + len(hLines)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	union := func(a, b int) { parent[find(a)] = find(b) }
	for i, v := range vLines {
		for j, h := range hLines {
			if v.pos >= h.from-ruleTol && v.pos <= h.to+ruleTol && h.pos >= v.from-ruleTol && h.pos <= v.to+ruleTol {
				union(i, len(vLines)+j)
			}
		}
	}

	type members struct {
		vs     []gridLine
		ys     []float64
		colors map[string]int
	}
	mem := map[int]*members{}
	get := func(r int) *members {
		if mem[r] == nil {
			mem[r] = &members{colors: map[string]int{}}
		}
		return mem[r]
	}
	for i, v := range vLines {
		m := get(find(i))
		m.vs = append(m.vs, v)
		m.colors[v.color]++
	}
	for j, h := range hLines {
		m := get(find(len(vLines) + j))
		m.ys = append(m.ys, h.pos)
		m.colors[h.color]++
	}

	var tables []table
	for _, m := range mem {
		if len(m.vs) < 2 || len(m.ys) < 2 {
			continue
		}
		xs := dedupe(positions(m.vs), true)
		ys := dedupe(m.ys, false)
		if len(xs) < 2 || len(ys) < 2 {
			continue
		}
		t := table{x0: xs[0], x1: xs[len(xs)-1], y1: ys[0], y0: ys[len(ys)-1], cols: xs, rows: ys, vlines: m.vs}
		if t.x1-t.x0 < 2*ruleMinLength || t.y1-t.y0 < 2*ruleMinLength {
			continue
		}
		best := -1
		for c, cnt := range m.colors {
			if cnt > best {
				best, t.color = cnt, c
			}
		}
		t.buildSlots()
		tables = append(tables, t)
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].y1 > tables[j].y1 })
	return tables
}

func positions(gs []gridLine) []float64 {
	out := make([]float64, len(gs))
	for i, g := range gs {
		out[i] = g.pos
	}
	return out
}

// dedupe sorts positions (ascending, or descending when desc) and merges
// those within ruleTol.
func dedupe(ps []float64, ascending bool) []float64 {
	sort.Float64s(ps)
	var out []float64
	for _, p := range ps {
		if n := len(out); n > 0 && math.Abs(out[n-1]-p) <= ruleTol {
			out[n-1] = (out[n-1] + p) / 2
			continue
		}
		out = append(out, p)
	}
	if !ascending {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out
}

// buildSlots derives each row's cells: a column boundary with no vertical
// ruling inside the row merges the neighbouring cells.
func (t *table) buildSlots() {
	ncols := len(t.cols) - 1
	t.cells = make([][]cellSlot, len(t.rows)-1)
	for r := range t.cells {
		top, bottom := t.rows[r], t.rows[r+1]
		var slots []cellSlot
		for c := 0; c < ncols; {
			span := 1
			for c+span < ncols && !t.hasVertical(t.cols[c+span], bottom, top) {
				span++
			}
			slots = append(slots, cellSlot{col: c, span: span})
			c += span
		}
		t.cells[r] = slots
	}
}

func (t *table) hasVertical(x, bottom, top float64) bool {
	for _, v := range t.vlines {
		if math.Abs(v.pos-x) <= ruleTol && v.covers(bottom, top) {
			return true
		}
	}
	return false
}

// slotFor returns the cell slot of row r containing grid column ci.
func (t *table) slotFor(r, ci int) *cellSlot {
	for i := range t.cells[r] {
		s := &t.cells[r][i]
		if ci >= s.col && ci < s.col+s.span {
			return s
		}
	}
	return &t.cells[r][len(t.cells[r])-1]
}

// assignLines places text lines whose segments fall inside a table into its
// cells (segment by segment) and returns the lines that stay in the flow.
func assignLines(lines []textLine, tables []table) []textLine {
	if len(tables) == 0 {
		return lines
	}
	var rest []textLine
	for _, ln := range lines {
		var free []segment
		for _, s := range ln.segments {
			cx, cy := (s.x0+s.x1)/2, (ln.y0+ln.y1)/2
			placed := false
			for ti := range tables {
				t := &tables[ti]
				if !t.contains(cx, cy) {
					continue
				}
				ci := sort.SearchFloat64s(t.cols, cx) - 1
				ci = int(math.Max(0, math.Min(float64(len(t.cols)-2), float64(ci))))
				ri := 0
				for ri < len(t.rows)-2 && cy < t.rows[ri+1] {
					ri++
				}
				cellLine := textLine{x0: s.x0, x1: s.x1, y0: ln.y0, y1: ln.y1, size: ln.size, bold: ln.bold, segments: []segment{s}}
				slot := t.slotFor(ri, ci)
				slot.lines = append(slot.lines, cellLine)
				placed = true
				break
			}
			if !placed {
				free = append(free, s)
			}
		}
		if len(free) == len(ln.segments) {
			rest = append(rest, ln)
		} else if len(free) > 0 {
			l2 := ln
			l2.segments = free
			l2.x0, l2.x1 = free[0].x0, free[len(free)-1].x1
			rest = append(rest, l2)
		}
	}
	return rest
}

// tableBlock converts a detected table into a model block.
func tableBlock(t table, ct content) model.Block {
	td := &model.TableData{Ruled: true, BorderColor: t.color}
	for i := 0; i < len(t.cols)-1; i++ {
		td.ColWidths = append(td.ColWidths, t.cols[i+1]-t.cols[i])
	}
	for ri := range t.cells {
		var row []model.Cell
		for _, slot := range t.cells[ri] {
			lines := slot.lines
			sort.SliceStable(lines, func(a, b int) bool {
				if math.Abs(lines[a].y1-lines[b].y1) > 1 {
					return lines[a].y1 > lines[b].y1
				}
				return lines[a].x0 < lines[b].x0
			})
			cellLeft, cellRight := t.cols[slot.col], t.cols[slot.col+slot.span]
			cell := model.Cell{Span: slot.span}
			centered, righted := len(lines) > 0, len(lines) > 0
			for _, l := range lines {
				var segs []model.Segment
				for _, s := range l.segments {
					segs = append(segs, model.Segment{X: math.Max(0, s.x0-cellLeft), Runs: s.runs})
				}
				cell.Lines = append(cell.Lines, model.Line{Segments: segs})
				mid := (cellLeft + cellRight) / 2
				if math.Abs((l.x0+l.x1)/2-mid) > 0.08*(cellRight-cellLeft)+1 {
					centered = false
				}
				if l.x1 < cellRight-4 || l.x0 < cellLeft+4 {
					righted = false
				}
			}
			switch {
			case centered && len(lines) > 0 && lines[0].x0 > cellLeft+3:
				cell.Align = model.AlignCenter
			case righted:
				cell.Align = model.AlignRight
			}
			row = append(row, cell)
		}
		td.Rows = append(td.Rows, row)
	}
	b := model.Block{Kind: model.Table, Table: td}
	if ind := t.x0 - ct.left; ind > 1.5 {
		b.IndentLeft = ind
	}
	return b
}
