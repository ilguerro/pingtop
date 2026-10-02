// Package ui renders the pingtop dashboard with Bubble Tea.
package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"

	"github.com/guerrieroriccardo/pingtop/internal/pinger"
)

// columnDef describes one table column. Order in the columns slice
// matches the cells emitted by buildRows. tier drives responsive
// hiding: tier 0 columns are always shown; higher-tier columns are
// dropped first when the terminal is too narrow to fit them all.
// sortable=true means the column participates in the `s` sort cycle.
type columnDef struct {
	header   string
	width    int
	tier     int
	sortable bool
}

// SENT/LOST is intentionally non-sortable: its only meaningful scalar
// is loss%, which the LOSS% column already covers — having both in the
// cycle would produce two identical orderings. SPARK has no scalar.
var columns = []columnDef{
	{header: "TARGET", width: 28, tier: 0, sortable: true},
	{header: "RTT", width: 10, tier: 0, sortable: true},
	{header: "MIN", width: 10, tier: 3, sortable: true},
	{header: "AVG", width: 10, tier: 3, sortable: true},
	{header: "MAX", width: 10, tier: 3, sortable: true},
	{header: "JITTER", width: 10, tier: 0, sortable: true},
	{header: "LOSS%", width: 8, tier: 0, sortable: true},
	{header: "SENT/LOST", width: 12, tier: 2, sortable: false},
	{header: "SPARK", width: sparkWidth + 2, tier: 1, sortable: false},
}

// statsMsg wraps a StatsUpdate so it can flow through the Bubble Tea
// message bus without exposing pinger types as a top-level tea.Msg.
type statsMsg pinger.StatsUpdate

// Model is the dashboard state. Construct it with New, then pass it to
// tea.NewProgram.
type Model struct {
	order       []string
	updates     <-chan pinger.StatsUpdate
	stats       map[string]pinger.StatsUpdate
	history     map[string][]time.Duration // per-target RTT ring buffer for the sparkline
	termWidth   int                        // last WindowSizeMsg width; 0 until first event (renders all columns)
	termHeight  int                        // last WindowSizeMsg height; 0 until first event
	offset      int                        // first row index shown when content overflows viewport
	filterMode  bool                       // true while user is typing into the filter
	filter      string                     // active filter; empty == no filter
	sortCol     int                        // -1 = no sort (insertion order); otherwise index into columns
	sortDesc    bool                       // direction when sortCol >= 0; default true (worst first)
	keepDropped bool                       // if true, dropped rows stay visible with final stats
	styler      styler                     // colors for RTT/JITTER/LOSS%; disabled styler is a no-op
}

// New builds the initial model. ids is the stable display order
// produced by target.Expand; updates is the shared channel the
// pingers publish on. If keepDropped is true, rows for targets that
// hit the drop threshold stay visible with their final (100% loss)
// stats instead of being removed. If colorize is true, the RTT,
// JITTER, and LOSS% cells are tinted by threshold.
func New(ids []string, updates <-chan pinger.StatsUpdate, keepDropped, colorize bool) Model {
	return Model{
		order:       ids,
		updates:     updates,
		stats:       make(map[string]pinger.StatsUpdate, len(ids)),
		history:     make(map[string][]time.Duration, len(ids)),
		sortCol:     -1,
		sortDesc:    true,
		keepDropped: keepDropped,
		styler:      newStyler(colorize),
	}
}

func (m Model) Init() tea.Cmd {
	return m.waitForUpdate()
}

// waitForUpdate returns a Cmd that blocks on the updates channel and
// turns the next event into a statsMsg. When the channel is closed
// (main has stopped all pingers) it emits tea.Quit so the program
// exits cleanly.
func (m Model) waitForUpdate() tea.Cmd {
	ch := m.updates
	return func() tea.Msg {
		u, ok := <-ch
		if !ok {
			return tea.Quit()
		}
		return statsMsg(u)
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case statsMsg:
		if msg.Dropped {
			if m.keepDropped {
				// Persist the final snapshot (Sent=N, Recv=0) so the
				// row keeps showing 100% loss after the pinger stops.
				m.stats[msg.TargetID] = pinger.StatsUpdate(msg)
			} else {
				delete(m.stats, msg.TargetID)
				delete(m.history, msg.TargetID)
				m.order = removeID(m.order, msg.TargetID)
				clampOffset(&m)
			}
			return m, m.waitForUpdate()
		}
		m.stats[msg.TargetID] = pinger.StatsUpdate(msg)
		if msg.RTT > 0 {
			appendHistory(m.history, msg.TargetID, msg.RTT)
		}
		return m, m.waitForUpdate()

	case tea.KeyMsg:
		if m.filterMode {
			switch msg.Type {
			case tea.KeyCtrlC:
				return m, tea.Quit
			case tea.KeyEsc:
				m.filterMode = false
				m.filter = ""
				clampOffset(&m)
			case tea.KeyEnter:
				m.filterMode = false
			case tea.KeyBackspace:
				if len(m.filter) > 0 {
					m.filter = m.filter[:len(m.filter)-1]
					clampOffset(&m)
				}
			case tea.KeySpace:
				m.filter += " "
				clampOffset(&m)
			case tea.KeyRunes:
				m.filter += string(msg.Runes)
				clampOffset(&m)
			}
			return m, nil
		}

		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "/":
			m.filterMode = true
			return m, nil
		case "esc":
			if m.filter != "" {
				m.filter = ""
				clampOffset(&m)
			}
			return m, nil
		case "up", "k":
			if m.offset > 0 {
				m.offset--
			}
			return m, nil
		case "down", "j":
			if m.offset < m.maxOffset() {
				m.offset++
			}
			return m, nil
		case "s":
			m.sortCol = nextSortCol(m.sortCol, visibleSortable(m.termWidth))
			clampOffset(&m)
			return m, nil
		case "S":
			m.sortCol = prevSortCol(m.sortCol, visibleSortable(m.termWidth))
			clampOffset(&m)
			return m, nil
		case "r":
			if m.sortCol >= 0 {
				m.sortDesc = !m.sortDesc
			}
			return m, nil
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		// If the active sort column just got hidden by the resize, drop
		// the sort. Predictable state beats invisible sort.
		if m.sortCol >= 0 && !columnVisible(m.sortCol, m.termWidth) {
			m.sortCol = -1
		}
		clampOffset(&m)
		// Resize cycles drop/add columns, which makes individual rows
		// expand or contract horizontally. Bubble Tea's diff renderer
		// can leave stale fragments visible during rapid resizes, so
		// force a clean repaint on every WindowSizeMsg.
		return m, tea.ClearScreen
	}
	return m, nil
}

func (m Model) View() string {
	var text string
	switch {
	case m.filterMode:
		text = fmt.Sprintf("/%s█  [enter] apply  [esc] clear", m.filter)
	case len(m.order) == 0:
		text = "no hosts reachable — [q] quit"
	case m.filter != "":
		text = fmt.Sprintf("filter: %s  [/] edit  [esc] clear  [q] quit", m.filter)
	case m.sortCol >= 0:
		text = fmt.Sprintf("[q] quit  [/] filter  [↑/↓] scroll  [s/S] sort: %s %s  [r] reverse",
			sortName(m.sortCol), sortArrow(m.sortDesc))
	default:
		text = "[q] quit  [/] filter  [↑/↓] scroll  [s/S] sort"
	}
	help := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		Render(text)
	return m.renderTable() + "\n" + help
}

// renderTable builds a fresh lipgloss/table on every call. lipgloss/table
// is a stateless renderer, not a Bubble Tea component, so we rebuild from
// current model state rather than holding a long-lived table instance.
func (m Model) renderTable() string {
	visible := visibleColumns(m.termWidth)
	sparkW := effectiveSparkWidth(m.termWidth)
	fullRows := buildRows(m.visibleIDs(), m.stats, m.history, m.styler, sparkW)

	headers := make([]string, len(visible))
	for i, ci := range visible {
		headers[i] = columns[ci].header
	}
	// Decorate the active sort column's header. The WindowSizeMsg arm
	// already reset sortCol to -1 if the column got hidden, so an arrow
	// here always lands on a visible column.
	if m.sortCol >= 0 {
		for i, ci := range visible {
			if ci == m.sortCol {
				headers[i] += " " + sortArrow(m.sortDesc)
			}
		}
	}
	// Slice the visible window ourselves rather than handing all rows to
	// lipgloss/table with Height()+Offset(). The library reserves the
	// bottom line for an ellipsis when there's overflow, which silently
	// hides the last data row even at maxOffset — on a /24 that's the
	// last two hosts you can never scroll to. By pre-trimming to the
	// window the table never enters overflow mode and every row stays
	// reachable.
	avail := m.visibleRowCount(len(fullRows))
	windowed := fullRows
	if avail < len(fullRows) {
		windowed = fullRows[m.offset : m.offset+avail]
	}
	rows := make([][]string, len(windowed))
	for r, row := range windowed {
		cells := make([]string, len(visible))
		for i, ci := range visible {
			cells[i] = row[ci]
		}
		rows[r] = cells
	}

	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	t := table.New().
		Border(lipgloss.NormalBorder()).
		BorderTop(false).
		BorderBottom(false).
		BorderLeft(false).
		BorderRight(false).
		BorderColumn(false).
		BorderHeader(true).
		BorderStyle(dim).
		Headers(headers...).
		Rows(rows...).
		StyleFunc(func(row, col int) lipgloss.Style {
			origCol := visible[col]
			w := columns[origCol].width
			if origCol == len(columns)-1 {
				w = sparkW + 2
			}
			s := lipgloss.NewStyle().
				Width(w).
				MaxWidth(w).
				PaddingRight(1)
			if row == table.HeaderRow {
				s = s.Bold(true)
			}
			return s
		})
	return t.String()
}

// visibleRowCount is how many data rows fit in the table area.
// Returns total when the terminal height isn't known yet so the first
// frame isn't truncated.
func (m Model) visibleRowCount(total int) int {
	if m.termHeight <= 0 {
		return total
	}
	avail := m.termHeight - 1 - headerLines
	if avail < 1 {
		avail = 1
	}
	if avail > total {
		return total
	}
	return avail
}

// visibleIDs returns m.order filtered by m.filter (case-insensitive
// substring) and then sorted by m.sortCol. When the filter is empty and
// no sort is active, it returns m.order directly.
func (m Model) visibleIDs() []string {
	var out []string
	if m.filter == "" {
		if m.sortCol < 0 {
			return m.order
		}
		out = append(out, m.order...)
	} else {
		f := strings.ToLower(m.filter)
		out = make([]string, 0, len(m.order))
		for _, id := range m.order {
			if strings.Contains(strings.ToLower(id), f) {
				out = append(out, id)
			}
		}
	}
	if m.sortCol < 0 {
		return out
	}
	// TARGET sort works on the ID string and doesn't need stats — keep
	// it out of the missing-stats sink path so every row gets compared.
	if m.sortCol == 0 {
		sort.SliceStable(out, func(i, j int) bool {
			if m.sortDesc {
				return out[i] > out[j]
			}
			return out[i] < out[j]
		})
		return out
	}
	sort.SliceStable(out, func(i, j int) bool {
		si, oki := m.stats[out[i]]
		sj, okj := m.stats[out[j]]
		// Targets without stats sink to the bottom regardless of direction.
		if !oki && !okj {
			return false
		}
		if !oki {
			return false
		}
		if !okj {
			return true
		}
		var less bool
		switch m.sortCol {
		case 1:
			less = si.RTT < sj.RTT
		case 2:
			less = si.MinRTT < sj.MinRTT
		case 3:
			less = si.AvgRTT < sj.AvgRTT
		case 4:
			less = si.MaxRTT < sj.MaxRTT
		case 5:
			less = si.Jitter < sj.Jitter
		case 6:
			less = lossPct(si) < lossPct(sj)
		}
		if m.sortDesc {
			return !less
		}
		return less
	})
	return out
}

// buildRows produces table rows in the stable order. A nil stats map
// renders all targets in their initial "no data yet" state. sparkW is
// the rendered sparkline width (see effectiveSparkWidth).
func buildRows(order []string, stats map[string]pinger.StatsUpdate, history map[string][]time.Duration, st styler, sparkW int) [][]string {
	rows := make([][]string, len(order))
	for i, id := range order {
		s, ok := stats[id]
		if !ok {
			rows[i] = []string{id, "—", "—", "—", "—", "—", "—", "—", formatSpark(nil, sparkW, st)}
			continue
		}
		rows[i] = []string{
			id,
			st.render(formatRTT(s), rttLevel(s)),
			formatDur(s.MinRTT),
			formatDur(s.AvgRTT),
			formatDur(s.MaxRTT),
			st.render(formatJitter(s), jitterLevel(s)),
			st.render(formatLoss(s), lossLevel(s)),
			formatSentLost(s),
			formatSpark(history[id], sparkW, st),
		}
	}
	return rows
}

func formatRTT(s pinger.StatsUpdate) string {
	if s.RTT > 0 {
		return s.RTT.Round(time.Microsecond).String()
	}
	if s.LastErr != nil {
		return "err"
	}
	return "—"
}

func formatJitter(s pinger.StatsUpdate) string {
	if s.Jitter > 0 {
		return s.Jitter.Round(time.Microsecond).String()
	}
	return "—"
}

// formatDur renders a Duration as a microsecond-rounded string, or "—"
// for zero. Used by the MIN/AVG/MAX columns which have no error branch.
func formatDur(d time.Duration) string {
	if d > 0 {
		return d.Round(time.Microsecond).String()
	}
	return "—"
}

func formatLoss(s pinger.StatsUpdate) string {
	if s.Sent == 0 {
		return "—"
	}
	pct := 100 * float64(s.Sent-s.Recv) / float64(s.Sent)
	return fmt.Sprintf("%.1f%%", pct)
}

// lossPct returns the loss percentage for sort comparison. Returns -1
// when Sent==0 so "no data" rows don't tie zero-loss winners.
func lossPct(s pinger.StatsUpdate) float64 {
	if s.Sent == 0 {
		return -1
	}
	return 100 * float64(s.Sent-s.Recv) / float64(s.Sent)
}

func formatSentLost(s pinger.StatsUpdate) string {
	if s.Sent == 0 {
		return "—"
	}
	lost := s.Sent - s.Recv
	if lost < 0 {
		lost = 0
	}
	return fmt.Sprintf("%d/%d", s.Sent, lost)
}

// level classifies a metric value into a color bucket. levelNeutral
// means "no data" / "no verdict" — styler renders it without color.
type level int

const (
	levelNeutral level = iota
	levelGood
	levelWarn
	levelCrit
)

// Threshold defaults: chosen to match common sysadmin intuition for
// LAN/WAN ping monitoring. Not configurable in v0.10; promote to flags
// if anyone asks.
const (
	rttWarn     = 50 * time.Millisecond
	rttCrit     = 200 * time.Millisecond
	jitterWarn  = 5 * time.Millisecond
	jitterCrit  = 20 * time.Millisecond
	lossCritPct = 5.0 // ≥ 5% loss is crit; anything > 0 and < 5 is warn
)

func rttLevel(s pinger.StatsUpdate) level {
	if s.RTT > 0 {
		switch {
		case s.RTT < rttWarn:
			return levelGood
		case s.RTT < rttCrit:
			return levelWarn
		default:
			return levelCrit
		}
	}
	if s.LastErr != nil {
		return levelCrit
	}
	return levelNeutral
}

func jitterLevel(s pinger.StatsUpdate) level {
	if s.Jitter <= 0 {
		return levelNeutral
	}
	switch {
	case s.Jitter < jitterWarn:
		return levelGood
	case s.Jitter < jitterCrit:
		return levelWarn
	default:
		return levelCrit
	}
}

func lossLevel(s pinger.StatsUpdate) level {
	if s.Sent == 0 {
		return levelNeutral
	}
	pct := 100 * float64(s.Sent-s.Recv) / float64(s.Sent)
	switch {
	case pct == 0:
		return levelGood
	case pct < lossCritPct:
		return levelWarn
	default:
		return levelCrit
	}
}

// styler wraps cell strings in lipgloss color styles. The zero value
// (enabled=false) is a no-op renderer, so passing styler{} disables
// coloring without special-casing callers.
type styler struct {
	enabled    bool
	good, warn, crit lipgloss.Style
}

func newStyler(enabled bool) styler {
	if !enabled {
		return styler{}
	}
	return styler{
		enabled: true,
		good:    lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
		warn:    lipgloss.NewStyle().Foreground(lipgloss.Color("11")),
		crit:    lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
	}
}

func (st styler) render(s string, l level) string {
	if !st.enabled || l == levelNeutral {
		return s
	}
	switch l {
	case levelGood:
		return st.good.Render(s)
	case levelWarn:
		return st.warn.Render(s)
	case levelCrit:
		return st.crit.Render(s)
	}
	return s
}

// headerLines counts the rows lipgloss/table renders above the data:
// one for the header titles and one for the border-bottom separator.
const headerLines = 2

// maxOffset is the highest offset that still keeps the last row in
// view. Below this, every row is visible without scrolling.
func (m Model) maxOffset() int {
	if m.termHeight <= 0 {
		return 0
	}
	// Available data rows = termHeight - help line - header lines.
	avail := m.termHeight - 1 - headerLines
	if avail < 1 {
		avail = 1
	}
	rows := len(m.visibleIDs())
	if rows <= avail {
		return 0
	}
	return rows - avail
}

// clampOffset reins offset back in after the visible set or terminal
// height changes (filter edit, dropped row, window resize). Called from
// Update; safe to invoke whenever m.offset might have gone stale.
func clampOffset(m *Model) {
	if max := m.maxOffset(); m.offset > max {
		m.offset = max
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

// visibleColumns returns the indices into `columns` that fit within
// termWidth, dropping by tier (highest tier first) until they fit or
// only tier-0 columns remain. termWidth <= 0 means the program hasn't
// received a WindowSizeMsg yet; render every column so the first frame
// isn't accidentally truncated.
func visibleColumns(termWidth int) []int {
	if termWidth <= 0 {
		idx := make([]int, len(columns))
		for i := range idx {
			idx[i] = i
		}
		return idx
	}
	hidden := make(map[int]bool)
	total := func() int {
		sum := 0
		for i, c := range columns {
			if !hidden[i] {
				sum += c.width
			}
		}
		return sum
	}
	maxTier := 0
	for _, c := range columns {
		if c.tier > maxTier {
			maxTier = c.tier
		}
	}
	for tier := maxTier; tier > 0 && total() > termWidth; tier-- {
		for i, c := range columns {
			if c.tier == tier {
				hidden[i] = true
			}
		}
	}
	out := make([]int, 0, len(columns))
	for i := range columns {
		if !hidden[i] {
			out = append(out, i)
		}
	}
	return out
}

func sortName(col int) string {
	if col < 0 || col >= len(columns) {
		return ""
	}
	return strings.ToLower(columns[col].header)
}

func sortArrow(desc bool) string {
	if desc {
		return "↓"
	}
	return "↑"
}

// visibleSortable returns the indices of columns that are both
// currently visible (per termWidth) and marked sortable.
func visibleSortable(termWidth int) []int {
	out := []int{}
	for _, ci := range visibleColumns(termWidth) {
		if columns[ci].sortable {
			out = append(out, ci)
		}
	}
	return out
}

// nextSortCol advances current through sortable, wrapping to -1 after
// the last entry. If current is -1 or not in sortable (because it just
// got hidden), restart at the first entry.
func nextSortCol(current int, sortable []int) int {
	if len(sortable) == 0 {
		return -1
	}
	for i, ci := range sortable {
		if ci == current {
			if i+1 < len(sortable) {
				return sortable[i+1]
			}
			return -1
		}
	}
	return sortable[0]
}

// prevSortCol walks sortable in reverse, mirroring nextSortCol. From -1
// (or a hidden column) it jumps to the last entry; from the first entry
// it wraps back to -1.
func prevSortCol(current int, sortable []int) int {
	if len(sortable) == 0 {
		return -1
	}
	for i, ci := range sortable {
		if ci == current {
			if i > 0 {
				return sortable[i-1]
			}
			return -1
		}
	}
	return sortable[len(sortable)-1]
}

// columnVisible reports whether the given column index is currently on
// screen given termWidth.
func columnVisible(col, termWidth int) bool {
	for _, ci := range visibleColumns(termWidth) {
		if ci == col {
			return true
		}
	}
	return false
}

func removeID(order []string, id string) []string {
	for i, s := range order {
		if s == id {
			return append(order[:i], order[i+1:]...)
		}
	}
	return order
}

// sparkWidth is the default number of recent RTT samples shown in the
// SPARK column. At a 1 s interval this is also the seconds of visible
// history. On wide terminals the column expands up to maxSparkWidth to
// claim leftover horizontal space.
const sparkWidth = 20
const maxSparkWidth = 200

// sparkBars is the 8-level Unicode bar set used to render samples.
var sparkBars = []rune("▁▂▃▄▅▆▇█")

// sparkErr is the history sentinel for a probe that failed with a
// network error. Real RTT samples are always > 0.
const sparkErr time.Duration = -1

// sparkErrPlain marks a sparkErr sample when color is off, where a
// full-height bar would be indistinguishable from a slow reply.
const sparkErrPlain = "×"

func appendHistory(h map[string][]time.Duration, id string, rtt time.Duration) {
	buf := h[id]
	if len(buf) >= maxSparkWidth {
		buf = buf[1:]
	}
	h[id] = append(buf, rtt)
}

// formatSpark renders the recent RTT samples as a Unicode bar chart,
// scaled per-target between the window's min and max so relative
// jitter is what's visible. Pads with leading spaces until the buffer
// fills, so the latest sample is always at the right edge. width sets
// the rendered column width (number of bar cells). sparkErr samples
// are left out of the scaling and drawn as a full-height bar in st's
// crit color, or as sparkErrPlain when st is disabled.
func formatSpark(history []time.Duration, width int, st styler) string {
	if width <= 0 {
		width = sparkWidth
	}
	if len(history) == 0 {
		return strings.Repeat(" ", width)
	}
	if len(history) > width {
		history = history[len(history)-width:]
	}
	var min, max time.Duration
	seen := false
	for _, d := range history {
		if d == sparkErr {
			continue
		}
		if !seen || d < min {
			min = d
		}
		if !seen || d > max {
			max = d
		}
		seen = true
	}
	rng := max - min

	errBar := sparkErrPlain
	if st.enabled {
		errBar = st.render(string(sparkBars[len(sparkBars)-1]), levelCrit)
	}

	var b strings.Builder
	b.Grow(width * 4) // bars are 3-byte UTF-8 runes
	for i := 0; i < width-len(history); i++ {
		b.WriteByte(' ')
	}
	for _, d := range history {
		if d == sparkErr {
			b.WriteString(errBar)
			continue
		}
		idx := len(sparkBars) / 2
		if rng > 0 {
			idx = int(int64(d-min) * int64(len(sparkBars)-1) / int64(rng))
			if idx < 0 {
				idx = 0
			}
			if idx >= len(sparkBars) {
				idx = len(sparkBars) - 1
			}
		}
		b.WriteRune(sparkBars[idx])
	}
	return b.String()
}

// effectiveSparkWidth returns how many sparkline bars the SPARK column
// should render given the current terminal width. It claims any
// horizontal slack left over after the visible columns lay out, capped
// at maxSparkWidth. Returns sparkWidth (the default) when SPARK isn't
// visible or termWidth hasn't been received yet.
func effectiveSparkWidth(termWidth int) int {
	if termWidth <= 0 {
		return sparkWidth
	}
	sparkIdx := len(columns) - 1
	visible := visibleColumns(termWidth)
	sparkOn := false
	used := 0
	for _, ci := range visible {
		used += columns[ci].width
		if ci == sparkIdx {
			sparkOn = true
		}
	}
	if !sparkOn {
		return sparkWidth
	}
	slack := termWidth - used
	if slack <= 0 {
		return sparkWidth
	}
	w := sparkWidth + slack
	if w > maxSparkWidth {
		return maxSparkWidth
	}
	return w
}
