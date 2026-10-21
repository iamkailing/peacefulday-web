package app

import (
	"fmt"
	"html"
	"html/template"
	"math"
	"strings"
)

// Server-rendered, dependency-free charts. Marks are HTML so labels stay crisp at any width;
// every chart ships a hover/focus tooltip (data-tip, wired in admin.js) and a table view.

type point struct {
	Key   string // machine key, e.g. 2026-10-21
	Label string // short axis label
	Long  string // tooltip / table label
	Value int64
	Prev  int64
}

// niceScale returns a rounded axis maximum and step for 4 gridlines.
func niceScale(max int64) (top, step int64) {
	if max <= 0 {
		return 4, 1
	}
	raw := float64(max) / 4
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	var nice float64
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if raw <= m*mag {
			nice = m * mag
			break
		}
	}
	step = int64(math.Ceil(nice))
	if step < 1 {
		step = 1
	}
	return step * 4, step
}

// compact formats an axis tick: 0 / 950 / 1.2K / 15K / 1.5M.
func compact(v int64) string {
	f := float64(v)
	switch {
	case v >= 1_000_000:
		return trimZero(fmt.Sprintf("%.1f", f/1_000_000)) + "M"
	case v >= 1_000:
		return trimZero(fmt.Sprintf("%.1f", f/1_000)) + "K"
	}
	return fmt.Sprint(v)
}

func trimZero(s string) string { return strings.TrimSuffix(s, ".0") }

// compactBaht formats satang as a compact baht tick.
func compactBaht(satang int64) string { return "฿" + compact(satang/100) }

type colChartOpts struct {
	Name         string // accessible name (chart title)
	Fmt          func(int64) string
	Tick         func(int64) string
	Compare      bool   // draw Prev as a de-emphasised line
	CurrentName  string // legend text for bars
	CompareName  string // legend text for line
	Height       int
	HighlightKey string
}

// colChart renders a single-series column chart, optionally with a previous-period line.
func colChart(pts []point, o colChartOpts) template.HTML {
	if o.Fmt == nil {
		o.Fmt = func(v int64) string { return fmt.Sprint(v) }
	}
	if o.Tick == nil {
		o.Tick = compact
	}
	if o.Height == 0 {
		o.Height = 180
	}
	var b strings.Builder
	if len(pts) == 0 {
		return template.HTML(`<p class="chart-empty">ยังไม่มีข้อมูลในช่วงนี้</p>`)
	}
	var max int64
	allZero := true
	for _, p := range pts {
		if p.Value > max {
			max = p.Value
		}
		if o.Compare && p.Prev > max {
			max = p.Prev
		}
		if p.Value != 0 || (o.Compare && p.Prev != 0) {
			allZero = false
		}
	}
	top, step := niceScale(max)
	esc := html.EscapeString
	fmt.Fprintf(&b, `<figure class="cc" role="group" aria-label="%s">`, esc(o.Name))
	if o.Compare {
		fmt.Fprintf(&b, `<figcaption class="cc-legend"><span><i class="key-bar"></i>%s</span><span><i class="key-line"></i>%s</span></figcaption>`, esc(o.CurrentName), esc(o.CompareName))
	}
	fmt.Fprintf(&b, `<div class="cc-body" style="--h:%dpx">`, o.Height)
	b.WriteString(`<div class="cc-y" aria-hidden="true">`)
	for i := 4; i >= 0; i-- {
		fmt.Fprintf(&b, `<span style="bottom:%d%%">%s</span>`, i*25, esc(o.Tick(step*int64(i))))
	}
	b.WriteString(`</div><div class="cc-plot">`)
	for i := 0; i <= 4; i++ {
		fmt.Fprintf(&b, `<i class="cc-grid" style="bottom:%d%%"></i>`, i*25)
	}
	fmt.Fprintf(&b, `<div class="cc-cols" style="grid-template-columns:repeat(%d,1fr)">`, len(pts))
	for _, p := range pts {
		h := float64(p.Value) * 100 / float64(top)
		tip := p.Long + " · " + o.Fmt(p.Value)
		if o.Compare {
			tip += " (" + o.CompareName + " " + o.Fmt(p.Prev) + ")"
		}
		cls := "cc-col"
		if p.Key != "" && p.Key == o.HighlightKey {
			cls += " is-current"
		}
		fmt.Fprintf(&b, `<div class="%s" tabindex="0" data-tip="%s" aria-label="%s"><i style="height:%.2f%%"></i></div>`, cls, esc(tip), esc(tip), h)
	}
	b.WriteString(`</div>`)
	if o.Compare && len(pts) > 1 {
		n := len(pts)
		fmt.Fprintf(&b, `<svg class="cc-line" viewBox="0 0 %d 100" preserveAspectRatio="none" aria-hidden="true"><polyline points="`, n)
		for i, p := range pts {
			fmt.Fprintf(&b, "%.3f,%.3f ", float64(i)+0.5, 100-float64(p.Prev)*100/float64(top))
		}
		b.WriteString(`"/></svg>`)
	}
	if allZero {
		b.WriteString(`<p class="cc-zero">ยังไม่มีข้อมูลในช่วงนี้</p>`)
	}
	b.WriteString(`</div></div>`)
	// x labels: at most ~8, always first and last
	every := int(math.Ceil(float64(len(pts)) / 8))
	show := map[int]bool{}
	last := len(pts) - 1
	for i := 0; i <= last; i += every {
		show[i] = true
	}
	if !show[last] {
		if lm := (last / every) * every; last-lm < (every+1)/2 && lm != 0 {
			delete(show, lm) // keep the last label readable instead of crowding the previous one
		}
		show[last] = true
	}
	fmt.Fprintf(&b, `<div class="cc-x" aria-hidden="true" style="grid-template-columns:repeat(%d,1fr)">`, len(pts))
	for i, p := range pts {
		lbl := ""
		if show[i] {
			lbl = p.Label
		}
		fmt.Fprintf(&b, `<span>%s</span>`, esc(lbl))
	}
	b.WriteString(`</div>`)
	b.WriteString(`<details class="cc-table"><summary>ดูเป็นตาราง</summary><table><thead><tr><th>ช่วง</th><th class="num">` + esc(nz(o.CurrentName, "ค่า")) + `</th>`)
	if o.Compare {
		b.WriteString(`<th class="num">` + esc(o.CompareName) + `</th>`)
	}
	b.WriteString(`</tr></thead><tbody>`)
	for _, p := range pts {
		fmt.Fprintf(&b, `<tr><td>%s</td><td class="num">%s</td>`, esc(p.Long), esc(o.Fmt(p.Value)))
		if o.Compare {
			fmt.Fprintf(&b, `<td class="num">%s</td>`, esc(o.Fmt(p.Prev)))
		}
		b.WriteString(`</tr>`)
	}
	b.WriteString(`</tbody></table></details></figure>`)
	return template.HTML(b.String())
}

func nz(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// barChart is the compact dashboard variant (single series, no comparison line).
func barChart(pts []point, _ int, h int, f func(int64) string) template.HTML {
	return colChart(pts, colChartOpts{Name: "รายได้รายวัน 14 วันล่าสุด", Fmt: f, Tick: compactBaht, Height: h, CurrentName: "รายได้สุทธิ", HighlightKey: DateOnly(Now()).Format("2006-01-02")})
}

type hbar struct {
	Label string
	Value int64
	Note  string // secondary text after the value, e.g. "12 การจอง"
}

// hbars renders a sorted horizontal bar list (single series, value at the tip).
func hbars(items []hbar, f func(int64) string, empty string) template.HTML {
	if len(items) == 0 {
		return template.HTML(`<p class="chart-empty">` + html.EscapeString(nz(empty, "ยังไม่มีข้อมูลในช่วงนี้")) + `</p>`)
	}
	var max int64
	for _, it := range items {
		if it.Value > max {
			max = it.Value
		}
	}
	var b strings.Builder
	b.WriteString(`<ul class="hb">`)
	for _, it := range items {
		w := 0.0
		if max > 0 {
			w = float64(it.Value) * 100 / float64(max)
		}
		val := f(it.Value)
		tip := it.Label + " · " + val
		if it.Note != "" {
			tip += " · " + it.Note
		}
		fmt.Fprintf(&b, `<li tabindex="0" data-tip="%s"><span class="hb-label">%s</span><span class="hb-track"><i style="width:%.2f%%"></i></span><span class="hb-val">%s`,
			html.EscapeString(tip), html.EscapeString(it.Label), w, html.EscapeString(val))
		if it.Note != "" {
			fmt.Fprintf(&b, ` <small>%s</small>`, html.EscapeString(it.Note))
		}
		b.WriteString(`</span></li>`)
	}
	b.WriteString(`</ul>`)
	return template.HTML(b.String())
}

// heatCell is one session-time × weekday cell.
type heatCell struct {
	Guests, Capacity int64
	Sessions         int64
}

var heatRamp = []string{"#f1f5ef", "#dcebd6", "#c2dcba", "#a3c99a", "#80b277", "#5e9858", "#437d41", "#2e6332", "#1f4a25"}

// heatmap renders occupancy by start time (rows) and weekday (columns) using one green ramp.
func heatmap(times []string, days []int, cells map[string]heatCell) template.HTML {
	if len(times) == 0 {
		return template.HTML(`<p class="chart-empty">ยังไม่มีรอบในช่วงนี้</p>`)
	}
	var b strings.Builder
	b.WriteString(`<div class="hm-wrap"><table class="hm"><thead><tr><th scope="col">เวลา</th>`)
	for _, d := range days {
		fmt.Fprintf(&b, `<th scope="col">%s</th>`, thDaysShort[d])
	}
	b.WriteString(`</tr></thead><tbody>`)
	for _, t := range times {
		fmt.Fprintf(&b, `<tr><th scope="row">%s</th>`, t)
		for _, d := range days {
			c, ok := cells[fmt.Sprintf("%d|%s", d, t)]
			if !ok || c.Capacity == 0 {
				b.WriteString(`<td class="hm-none" aria-label="ไม่มีรอบ">–</td>`)
				continue
			}
			pct := int(c.Guests * 100 / c.Capacity)
			idx := pct * (len(heatRamp) - 1) / 100
			if idx >= len(heatRamp) {
				idx = len(heatRamp) - 1
			}
			ink := "#1f2a24"
			if idx >= 5 {
				ink = "#ffffff"
			}
			tip := fmt.Sprintf("%s %s · %d/%d ที่ (%d%%) · %d รอบ", TH.WeekdayName(d), t, c.Guests, c.Capacity, pct, c.Sessions)
			fmt.Fprintf(&b, `<td tabindex="0" style="background:%s;color:%s" data-tip="%s" aria-label="%s">%d%%</td>`, heatRamp[idx], ink, html.EscapeString(tip), html.EscapeString(tip), pct)
		}
		b.WriteString(`</tr>`)
	}
	b.WriteString(`</tbody></table></div><div class="hm-scale" aria-hidden="true"><span>0%</span>`)
	for _, c := range heatRamp {
		fmt.Fprintf(&b, `<i style="background:%s"></i>`, c)
	}
	b.WriteString(`<span>100%</span></div>`)
	return template.HTML(b.String())
}
