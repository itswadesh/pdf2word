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
}

const (
	ruleTol       = 1.5 // pt: rules this close are the same grid line
	ruleMinLength = 6.0 // pt: shorter strokes are not rulings
	ruleMaxThick  = 2.0 // pt: thicker filled shapes are not rulings
)

// gridLine is a cluster of collinear rules.
type gridLine struct {
	pos      float64
	from, to float64
}

// cluster merges rules at (almost) the same position whose extents touch or
// overlap into grid lines, sorted by position.
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
		out = append(out, gridLine{pos: r.pos, from: r.from, to: r.to})
	}
	return out
}

// table is a detected lattice with text assigned to cells.
type table struct {
	x0, y0, x1, y1 float64
	cols, rows     []float64 // boundaries: cols ascending x, rows descending y (top first)
	cells          [][][]textLine
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

	groups := map[int]*table{}
	type members struct{ xs, ys []float64 }
	mem := map[int]*members{}
	for i, v := range vLines {
		r := find(i)
		if mem[r] == nil {
			mem[r] = &members{}
		}
		mem[r].xs = append(mem[r].xs, v.pos)
	}
	for j, h := range hLines {
		r := find(len(vLines) + j)
		if mem[r] == nil {
			mem[r] = &members{}
		}
		mem[r].ys = append(mem[r].ys, h.pos)
	}
	var tables []table
	for r, m := range mem {
		if len(m.xs) < 2 || len(m.ys) < 2 {
			continue
		}
		sort.Float64s(m.xs)
		sort.Sort(sort.Reverse(sort.Float64Slice(m.ys)))
		t := table{x0: m.xs[0], x1: m.xs[len(m.xs)-1], y1: m.ys[0], y0: m.ys[len(m.ys)-1], cols: m.xs, rows: m.ys}
		if t.x1-t.x0 < 2*ruleMinLength || t.y1-t.y0 < 2*ruleMinLength {
			continue
		}
		groups[r] = &t
		tables = append(tables, t)
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].y1 > tables[j].y1 })
	return tables
}

// assignLines places text lines whose segments fall inside a table into its
// cells (segment by segment) and returns the lines that stay in the flow.
func assignLines(lines []textLine, tables []table) []textLine {
	if len(tables) == 0 {
		return lines
	}
	for ti := range tables {
		t := &tables[ti]
		t.cells = make([][][]textLine, len(t.rows)-1)
		for r := range t.cells {
			t.cells[r] = make([][]textLine, len(t.cols)-1)
		}
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
				ri := 0
				for ri < len(t.rows)-2 && cy < t.rows[ri+1] {
					ri++
				}
				if ci < 0 {
					ci = 0
				}
				if ci > len(t.cols)-2 {
					ci = len(t.cols) - 2
				}
				cellLine := textLine{x0: s.x0, x1: s.x1, y0: ln.y0, y1: ln.y1, size: ln.size, bold: ln.bold, segments: []segment{s}}
				t.cells[ri][ci] = append(t.cells[ri][ci], cellLine)
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
	td := &model.TableData{Ruled: true}
	for i := 0; i < len(t.cols)-1; i++ {
		td.ColWidths = append(td.ColWidths, t.cols[i+1]-t.cols[i])
	}
	for ri := range t.cells {
		row := make([]model.Cell, len(t.cols)-1)
		for ci := range t.cells[ri] {
			lines := t.cells[ri][ci]
			sort.SliceStable(lines, func(a, b int) bool {
				if math.Abs(lines[a].y1-lines[b].y1) > 1 {
					return lines[a].y1 > lines[b].y1
				}
				return lines[a].x0 < lines[b].x0
			})
			cell := model.Cell{}
			cellLeft, cellRight := t.cols[ci], t.cols[ci+1]
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
			row[ci] = cell
		}
		td.Rows = append(td.Rows, row)
	}
	b := model.Block{Kind: model.Table, Table: td}
	if ind := t.x0 - ct.left; ind > 1.5 {
		b.IndentLeft = ind
	}
	return b
}
