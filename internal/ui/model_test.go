package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/guerrieroriccardo/pingtop/internal/pinger"
)

func TestFormatRTT(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    pinger.StatsUpdate
		want string
	}{
		{"no data", pinger.StatsUpdate{}, "—"},
		{"with rtt", pinger.StatsUpdate{RTT: 2*time.Millisecond + 500*time.Microsecond}, "2.5ms"},
		{"err sticky", pinger.StatsUpdate{LastErr: errors.New("boom")}, "err"},
		{"rtt overrides err", pinger.StatsUpdate{RTT: time.Millisecond, LastErr: errors.New("boom")}, "1ms"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatRTT(tc.s); got != tc.want {
				t.Errorf("formatRTT = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatJitter(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    pinger.StatsUpdate
		want string
	}{
		{"no data", pinger.StatsUpdate{}, "—"},
		{"with jitter", pinger.StatsUpdate{Jitter: 750 * time.Microsecond}, "750µs"},
		{"err has no effect", pinger.StatsUpdate{LastErr: errors.New("boom")}, "—"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatJitter(tc.s); got != tc.want {
				t.Errorf("formatJitter = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatLoss(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    pinger.StatsUpdate
		want string
	}{
		{"no sends yet", pinger.StatsUpdate{}, "—"},
		{"all received", pinger.StatsUpdate{Sent: 10, Recv: 10}, "0.0%"},
		{"half lost", pinger.StatsUpdate{Sent: 10, Recv: 5}, "50.0%"},
		{"all lost", pinger.StatsUpdate{Sent: 5, Recv: 0}, "100.0%"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatLoss(tc.s); got != tc.want {
				t.Errorf("formatLoss = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFormatSentLost(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    pinger.StatsUpdate
		want string
	}{
		{"no data", pinger.StatsUpdate{}, "—"},
		{"all received", pinger.StatsUpdate{Sent: 10, Recv: 10}, "10/0"},
		{"two lost", pinger.StatsUpdate{Sent: 10, Recv: 8}, "10/2"},
		{"recv exceeds sent clamps", pinger.StatsUpdate{Sent: 5, Recv: 6}, "5/0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatSentLost(tc.s); got != tc.want {
				t.Errorf("formatSentLost = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildRowsInitial(t *testing.T) {
	rows := buildRows([]string{"1.1.1.1", "8.8.8.8"}, nil, nil, styler{}, sparkWidth)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[0][0] != "1.1.1.1" || rows[1][0] != "8.8.8.8" {
		t.Errorf("rows lost their order: %v", rows)
	}
	for _, r := range rows {
		if len(r) != 9 {
			t.Errorf("expected 9 cells, got %d in %v", len(r), r)
			continue
		}
		for i := 1; i <= 7; i++ {
			if r[i] != "—" {
				t.Errorf("expected placeholder at index %d, got %q in %v", i, r[i], r)
			}
		}
	}
}

func TestRenderTableShowsLastRowsAtMaxOffset(t *testing.T) {
	updates := make(chan pinger.StatsUpdate)
	ids := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	m := New(ids, updates, false, false)
	m.termWidth = 200    // wide enough for all columns
	m.termHeight = 6     // 1 help + 2 header = 3 data rows visible
	m.offset = m.maxOffset()

	out := m.renderTable()
	// At maxOffset the last visible chunk must include "h" — the regression
	// was that lipgloss/table's overflow ellipsis swallowed the bottom row.
	if !strings.Contains(out, "h") {
		t.Errorf("expected last row 'h' visible at maxOffset, got:\n%s", out)
	}
	if !strings.Contains(out, "g") {
		t.Errorf("expected penultimate row 'g' visible at maxOffset, got:\n%s", out)
	}
}

func TestScrollWithinBounds(t *testing.T) {
	updates := make(chan pinger.StatsUpdate)
	m := New([]string{"a", "b", "c", "d", "e"}, updates, false, false)
	m.termHeight = 5 // 5 lines total: 1 help + 2 header = 2 available data rows.

	// First down arrow scrolls; further presses cap at maxOffset.
	for i := 0; i < 10; i++ {
		mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = mm.(Model)
	}
	if m.offset != m.maxOffset() {
		t.Errorf("expected offset to clamp at maxOffset=%d, got %d", m.maxOffset(), m.offset)
	}

	// Up arrow walks it back to 0 and stays there.
	for i := 0; i < 20; i++ {
		mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
		m = mm.(Model)
	}
	if m.offset != 0 {
		t.Errorf("expected offset to clamp at 0 after up-spam, got %d", m.offset)
	}
}

func TestUpdateApplyingStatsMsg(t *testing.T) {
	updates := make(chan pinger.StatsUpdate, 4)
	m := New([]string{"1.1.1.1", "8.8.8.8"}, updates, false, false)

	mm, _ := m.Update(statsMsg{
		TargetID: "1.1.1.1",
		Sent:     4,
		Recv:     3,
		RTT:      5 * time.Millisecond,
		MinRTT:   1 * time.Millisecond,
		AvgRTT:   3 * time.Millisecond,
		MaxRTT:   7 * time.Millisecond,
	})
	got := mm.(Model).stats["1.1.1.1"]
	if got.RTT != 5*time.Millisecond || got.Sent != 4 || got.Recv != 3 {
		t.Errorf("stats not stored as expected: %+v", got)
	}
	if got.MinRTT != 1*time.Millisecond || got.AvgRTT != 3*time.Millisecond || got.MaxRTT != 7*time.Millisecond {
		t.Errorf("min/avg/max not stored: %+v", got)
	}

	view := mm.(Model).View()
	for _, want := range []string{"5ms", "1ms", "3ms", "7ms", "25.0%"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}
}

func TestFormatDur(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    time.Duration
		want string
	}{
		{"zero", 0, "—"},
		{"sub-ms", 750 * time.Microsecond, "750µs"},
		{"ms", 5 * time.Millisecond, "5ms"},
		{"fractional ms", 2*time.Millisecond + 500*time.Microsecond, "2.5ms"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatDur(tc.d); got != tc.want {
				t.Errorf("formatDur(%v) = %q, want %q", tc.d, got, tc.want)
			}
		})
	}
}

func TestUpdateRemovesDroppedTarget(t *testing.T) {
	updates := make(chan pinger.StatsUpdate, 4)
	m := New([]string{"1.1.1.1", "8.8.8.8", "9.9.9.9"}, updates, false, false)

	mm, _ := m.Update(statsMsg{TargetID: "8.8.8.8", Dropped: true})
	out := mm.(Model)

	if len(out.order) != 2 || out.order[0] != "1.1.1.1" || out.order[1] != "9.9.9.9" {
		t.Errorf("order should be [1.1.1.1 9.9.9.9], got %v", out.order)
	}
	if _, ok := out.stats["8.8.8.8"]; ok {
		t.Errorf("stats for dropped target should be removed")
	}
	view := out.View()
	if strings.Contains(view, "8.8.8.8") {
		t.Errorf("view should not show dropped target:\n%s", view)
	}
	if !strings.Contains(view, "1.1.1.1") || !strings.Contains(view, "9.9.9.9") {
		t.Errorf("view should still show survivors:\n%s", view)
	}
}

func TestUpdateQuitOnKey(t *testing.T) {
	updates := make(chan pinger.StatsUpdate)
	m := New([]string{"1.1.1.1"}, updates, false, false)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("q should produce a Cmd")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("q should produce tea.QuitMsg, got %T", cmd())
	}
}

func TestUpdateQuitOnClosedChannel(t *testing.T) {
	updates := make(chan pinger.StatsUpdate)
	close(updates)
	m := New([]string{"1.1.1.1"}, updates, false, false)

	cmd := m.Init()
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("closed channel should produce tea.QuitMsg, got %T", cmd())
	}
}

func TestFilterMatchesSubstring(t *testing.T) {
	updates := make(chan pinger.StatsUpdate)
	m := New([]string{"1.1.1.1", "8.8.8.8", "192.168.1.10"}, updates, false, false)

	m.filter = "8.8"
	v := m.visibleIDs()
	if len(v) != 1 || v[0] != "8.8.8.8" {
		t.Errorf("expected [8.8.8.8] for filter %q, got %v", m.filter, v)
	}

	m.filter = "."
	v = m.visibleIDs()
	if len(v) != 3 {
		t.Errorf("expected all 3 to match filter %q, got %v", m.filter, v)
	}

	m.filter = "xyz"
	v = m.visibleIDs()
	if len(v) != 0 {
		t.Errorf("expected no matches for filter %q, got %v", m.filter, v)
	}
}

func TestFilterCaseInsensitive(t *testing.T) {
	updates := make(chan pinger.StatsUpdate)
	m := New([]string{"host-A.example", "HOST-b.example"}, updates, false, false)

	m.filter = "host-a"
	v := m.visibleIDs()
	if len(v) != 1 || v[0] != "host-A.example" {
		t.Errorf("expected [host-A.example] for filter %q, got %v", m.filter, v)
	}
}

func TestFormatSparkEmpty(t *testing.T) {
	got := formatSpark(nil, sparkWidth, styler{})
	if got != strings.Repeat(" ", sparkWidth) {
		t.Errorf("empty history should render as %d spaces, got %q", sparkWidth, got)
	}
}

func TestFormatSparkAllEqual(t *testing.T) {
	h := []time.Duration{10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond}
	got := formatSpark(h, sparkWidth, styler{})
	mid := string(sparkBars[len(sparkBars)/2])
	// Three middle bars, padded on the left to sparkWidth.
	want := strings.Repeat(" ", sparkWidth-3) + strings.Repeat(mid, 3)
	if got != want {
		t.Errorf("equal samples should all map to middle bar\n got=%q\nwant=%q", got, want)
	}
}

func TestFormatSparkScalesMinMax(t *testing.T) {
	h := []time.Duration{1 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond}
	got := formatSpark(h, sparkWidth, styler{})
	runes := []rune(got)
	// Last three runes are the data; min should be first bar, max should be last bar.
	last3 := runes[len(runes)-3:]
	if last3[0] != sparkBars[0] {
		t.Errorf("min sample should map to %c, got %c", sparkBars[0], last3[0])
	}
	if last3[2] != sparkBars[len(sparkBars)-1] {
		t.Errorf("max sample should map to %c, got %c", sparkBars[len(sparkBars)-1], last3[2])
	}
}

func TestFormatSparkRespectsWidth(t *testing.T) {
	got := formatSpark(nil, 50, styler{})
	if got != strings.Repeat(" ", 50) {
		t.Errorf("empty history at width=50 should render as 50 spaces, got %d chars", len([]rune(got)))
	}
}

func TestFormatSparkErrPlain(t *testing.T) {
	h := []time.Duration{sparkErr, sparkErr}
	got := formatSpark(h, sparkWidth, styler{})
	want := strings.Repeat(" ", sparkWidth-2) + strings.Repeat(sparkErrPlain, 2)
	if got != want {
		t.Errorf("error samples should render as %q without color\n got=%q\nwant=%q", sparkErrPlain, got, want)
	}
}

func TestFormatSparkErrIgnoredInScaling(t *testing.T) {
	// The sentinel is negative; it must not become the window's min and
	// flatten the real samples.
	h := []time.Duration{1 * time.Millisecond, sparkErr, 100 * time.Millisecond}
	runes := []rune(formatSpark(h, sparkWidth, styler{}))
	last3 := runes[len(runes)-3:]
	if last3[0] != sparkBars[0] {
		t.Errorf("min sample should map to %c, got %c", sparkBars[0], last3[0])
	}
	if string(last3[1]) != sparkErrPlain {
		t.Errorf("error sample should render as %q, got %c", sparkErrPlain, last3[1])
	}
	if last3[2] != sparkBars[len(sparkBars)-1] {
		t.Errorf("max sample should map to %c, got %c", sparkBars[len(sparkBars)-1], last3[2])
	}
}

func TestFormatSparkErrColored(t *testing.T) {
	old := lipgloss.DefaultRenderer().ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	defer lipgloss.SetColorProfile(old)

	st := newStyler(true)
	got := formatSpark([]time.Duration{10 * time.Millisecond, sparkErr}, sparkWidth, st)
	full := string(sparkBars[len(sparkBars)-1])
	if !strings.HasSuffix(got, st.render(full, levelCrit)) {
		t.Errorf("error sample should render as a crit-colored full bar, got %q", got)
	}
	if strings.Contains(got, sparkErrPlain) {
		t.Errorf("colored spark should not use the plain marker, got %q", got)
	}
	if w := lipgloss.Width(got); w != sparkWidth {
		t.Errorf("colored spark should still be %d cells wide, got %d", sparkWidth, w)
	}
}

func TestAppendHistoryRingBuffer(t *testing.T) {
	h := make(map[string][]time.Duration)
	for i := 0; i < maxSparkWidth+5; i++ {
		appendHistory(h, "x", time.Duration(i)*time.Millisecond)
	}
	if len(h["x"]) != maxSparkWidth {
		t.Errorf("history should cap at %d samples, got %d", maxSparkWidth, len(h["x"]))
	}
	// The oldest 5 samples should have been evicted; the buffer's first
	// sample should be sample #5 (zero-indexed).
	if h["x"][0] != 5*time.Millisecond {
		t.Errorf("oldest sample should be 5ms, got %v", h["x"][0])
	}
}

func TestEffectiveSparkWidthClaimsSlack(t *testing.T) {
	// All 9 columns visible total to 28+10+10+10+10+10+8+12+(20+2)=120.
	// At termWidth=160 there are 40 chars of slack — spark should claim it.
	got := effectiveSparkWidth(160)
	want := sparkWidth + (160 - 120)
	if got != want {
		t.Errorf("at termWidth=160 spark should be %d, got %d", want, got)
	}
}

func TestEffectiveSparkWidthCapped(t *testing.T) {
	got := effectiveSparkWidth(10_000)
	if got != maxSparkWidth {
		t.Errorf("very wide terminal should cap spark at %d, got %d", maxSparkWidth, got)
	}
}

func TestEffectiveSparkWidthDefaultWhenNoSlack(t *testing.T) {
	// At termWidth=120 the columns exactly fill: no slack.
	got := effectiveSparkWidth(120)
	if got != sparkWidth {
		t.Errorf("at termWidth=120 (exact fit) spark should be default %d, got %d", sparkWidth, got)
	}
}

func TestEffectiveSparkWidthDefaultWhenSparkHidden(t *testing.T) {
	// At termWidth=70 the SPARK column gets hidden by the tier algorithm.
	got := effectiveSparkWidth(70)
	if got != sparkWidth {
		t.Errorf("when SPARK is hidden spark should fall back to default %d, got %d", sparkWidth, got)
	}
}

func TestUpdateAppendsHistoryOnRTT(t *testing.T) {
	updates := make(chan pinger.StatsUpdate, 4)
	m := New([]string{"1.1.1.1"}, updates, false, false)

	mm, _ := m.Update(statsMsg{TargetID: "1.1.1.1", Sent: 1, Recv: 1, RTT: 3 * time.Millisecond})
	out := mm.(Model)
	if len(out.history["1.1.1.1"]) != 1 || out.history["1.1.1.1"][0] != 3*time.Millisecond {
		t.Errorf("expected one 3ms sample, got %v", out.history["1.1.1.1"])
	}

	// An RTT=0 message (OnSend snapshot) should NOT append.
	mm, _ = out.Update(statsMsg{TargetID: "1.1.1.1", Sent: 2, Recv: 1, RTT: 0})
	out = mm.(Model)
	if len(out.history["1.1.1.1"]) != 1 {
		t.Errorf("RTT=0 message should not append, got %v", out.history["1.1.1.1"])
	}
}

func TestViewWhenAllTargetsDropped(t *testing.T) {
	updates := make(chan pinger.StatsUpdate, 4)
	m := New([]string{"1.1.1.1"}, updates, false, false)

	mm, _ := m.Update(statsMsg{TargetID: "1.1.1.1", Dropped: true})
	view := mm.(Model).View()
	if !strings.Contains(view, "no hosts reachable") {
		t.Errorf("view should show empty-state message, got:\n%s", view)
	}
}

func TestKeepDroppedRetainsRow(t *testing.T) {
	updates := make(chan pinger.StatsUpdate, 4)
	m := New([]string{"1.1.1.1", "8.8.8.8"}, updates, true, false)

	mm, _ := m.Update(statsMsg{TargetID: "8.8.8.8", Sent: 5, Recv: 0, Dropped: true})
	out := mm.(Model)

	if len(out.order) != 2 {
		t.Errorf("keep-dropped should preserve order, got %v", out.order)
	}
	got, ok := out.stats["8.8.8.8"]
	if !ok {
		t.Fatalf("stats for dropped target should be retained")
	}
	if got.Sent != 5 || got.Recv != 0 {
		t.Errorf("final stats should be (5,0), got (%d,%d)", got.Sent, got.Recv)
	}
	view := out.View()
	if !strings.Contains(view, "8.8.8.8") || !strings.Contains(view, "100.0%") || !strings.Contains(view, "5/5") {
		t.Errorf("view should show row with 100%% loss and 5/5, got:\n%s", view)
	}
}

func TestFilterUpdateOnSlashKey(t *testing.T) {
	updates := make(chan pinger.StatsUpdate, 4)
	m := New([]string{"1.1.1.1", "8.8.8.8"}, updates, false, false)

	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	out := mm.(Model)
	if !out.filterMode {
		t.Fatalf("expected filterMode=true after '/'")
	}

	mm, _ = out.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'8'}})
	out = mm.(Model)
	mm, _ = out.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'.'}})
	out = mm.(Model)
	if out.filter != "8." {
		t.Errorf("expected filter=%q, got %q", "8.", out.filter)
	}

	mm, _ = out.Update(tea.KeyMsg{Type: tea.KeyEsc})
	out = mm.(Model)
	if out.filterMode {
		t.Errorf("expected filterMode=false after esc")
	}
	if out.filter != "" {
		t.Errorf("expected filter cleared after esc, got %q", out.filter)
	}
}

func TestRTTLevel(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    pinger.StatsUpdate
		want level
	}{
		{"no data", pinger.StatsUpdate{}, levelNeutral},
		{"err only", pinger.StatsUpdate{LastErr: errors.New("boom")}, levelCrit},
		{"good", pinger.StatsUpdate{RTT: 30 * time.Millisecond}, levelGood},
		{"warn at boundary", pinger.StatsUpdate{RTT: 50 * time.Millisecond}, levelWarn},
		{"warn", pinger.StatsUpdate{RTT: 100 * time.Millisecond}, levelWarn},
		{"crit at boundary", pinger.StatsUpdate{RTT: 200 * time.Millisecond}, levelCrit},
		{"crit", pinger.StatsUpdate{RTT: 500 * time.Millisecond}, levelCrit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := rttLevel(tc.s); got != tc.want {
				t.Errorf("rttLevel = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestJitterLevel(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    pinger.StatsUpdate
		want level
	}{
		{"no data", pinger.StatsUpdate{}, levelNeutral},
		{"good", pinger.StatsUpdate{Jitter: 1 * time.Millisecond}, levelGood},
		{"warn", pinger.StatsUpdate{Jitter: 10 * time.Millisecond}, levelWarn},
		{"crit", pinger.StatsUpdate{Jitter: 50 * time.Millisecond}, levelCrit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := jitterLevel(tc.s); got != tc.want {
				t.Errorf("jitterLevel = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestLossLevel(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    pinger.StatsUpdate
		want level
	}{
		{"no sends", pinger.StatsUpdate{}, levelNeutral},
		{"zero loss", pinger.StatsUpdate{Sent: 100, Recv: 100}, levelGood},
		{"warn", pinger.StatsUpdate{Sent: 100, Recv: 99}, levelWarn},
		{"crit at boundary", pinger.StatsUpdate{Sent: 100, Recv: 95}, levelCrit},
		{"all lost", pinger.StatsUpdate{Sent: 5, Recv: 0}, levelCrit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := lossLevel(tc.s); got != tc.want {
				t.Errorf("lossLevel = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestStylerDisabled(t *testing.T) {
	st := newStyler(false)
	for _, l := range []level{levelNeutral, levelGood, levelWarn, levelCrit} {
		if got := st.render("hello", l); got != "hello" {
			t.Errorf("disabled styler should be a no-op, got %q for level %d", got, l)
		}
	}
}

func TestStylerEnabledAddsANSI(t *testing.T) {
	// lipgloss strips colors when stdout isn't a TTY (which it isn't
	// under `go test`). Force ANSI so the renderer actually emits codes.
	old := lipgloss.DefaultRenderer().ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	defer lipgloss.SetColorProfile(old)

	st := newStyler(true)
	if got := st.render("hello", levelNeutral); got != "hello" {
		t.Errorf("neutral should bypass coloring even when enabled, got %q", got)
	}
	for _, l := range []level{levelGood, levelWarn, levelCrit} {
		got := st.render("hello", l)
		if !strings.Contains(got, "\x1b[") {
			t.Errorf("level %d should add ANSI escape, got %q", l, got)
		}
		if !strings.Contains(got, "hello") {
			t.Errorf("level %d should preserve original text, got %q", l, got)
		}
	}
}

func TestSortIDsByRTT(t *testing.T) {
	updates := make(chan pinger.StatsUpdate)
	m := New([]string{"slow", "fast", "mid", "nodata"}, updates, false, false)
	m.stats["fast"] = pinger.StatsUpdate{RTT: 5 * time.Millisecond, Sent: 1, Recv: 1}
	m.stats["mid"] = pinger.StatsUpdate{RTT: 50 * time.Millisecond, Sent: 1, Recv: 1}
	m.stats["slow"] = pinger.StatsUpdate{RTT: 500 * time.Millisecond, Sent: 1, Recv: 1}

	m.sortCol = 1 // RTT
	m.sortDesc = true
	got := m.visibleIDs()
	want := []string{"slow", "mid", "fast", "nodata"}
	if !equalSlice(got, want) {
		t.Errorf("rtt desc: got %v, want %v", got, want)
	}

	m.sortDesc = false
	got = m.visibleIDs()
	want = []string{"fast", "mid", "slow", "nodata"}
	if !equalSlice(got, want) {
		t.Errorf("rtt asc: got %v, want %v", got, want)
	}
}

func TestSortIDsByLoss(t *testing.T) {
	updates := make(chan pinger.StatsUpdate)
	m := New([]string{"halflost", "clean", "alllost", "nodata"}, updates, false, false)
	m.stats["clean"] = pinger.StatsUpdate{Sent: 10, Recv: 10}
	m.stats["halflost"] = pinger.StatsUpdate{Sent: 10, Recv: 5}
	m.stats["alllost"] = pinger.StatsUpdate{Sent: 10, Recv: 0}

	m.sortCol = 6 // LOSS%
	m.sortDesc = true
	got := m.visibleIDs()
	want := []string{"alllost", "halflost", "clean", "nodata"}
	if !equalSlice(got, want) {
		t.Errorf("loss desc: got %v, want %v", got, want)
	}

	m.sortDesc = false
	got = m.visibleIDs()
	want = []string{"clean", "halflost", "alllost", "nodata"}
	if !equalSlice(got, want) {
		t.Errorf("loss asc: got %v, want %v", got, want)
	}
}

func TestSortIDsByTarget(t *testing.T) {
	updates := make(chan pinger.StatsUpdate)
	// Insertion order intentionally not alphabetical so we can tell
	// TARGET-sort from passthrough.
	m := New([]string{"charlie", "alpha", "bravo"}, updates, false, false)

	m.sortCol = 0 // TARGET
	m.sortDesc = false
	got := m.visibleIDs()
	want := []string{"alpha", "bravo", "charlie"}
	if !equalSlice(got, want) {
		t.Errorf("target asc: got %v, want %v", got, want)
	}

	m.sortDesc = true
	got = m.visibleIDs()
	want = []string{"charlie", "bravo", "alpha"}
	if !equalSlice(got, want) {
		t.Errorf("target desc: got %v, want %v", got, want)
	}
}

func TestSortIDsByJitter(t *testing.T) {
	updates := make(chan pinger.StatsUpdate)
	m := New([]string{"steady", "spiky", "mid"}, updates, false, false)
	m.stats["steady"] = pinger.StatsUpdate{Jitter: 1 * time.Millisecond, Sent: 1, Recv: 1}
	m.stats["mid"] = pinger.StatsUpdate{Jitter: 10 * time.Millisecond, Sent: 1, Recv: 1}
	m.stats["spiky"] = pinger.StatsUpdate{Jitter: 100 * time.Millisecond, Sent: 1, Recv: 1}

	m.sortCol = 5 // JITTER
	m.sortDesc = true
	got := m.visibleIDs()
	want := []string{"spiky", "mid", "steady"}
	if !equalSlice(got, want) {
		t.Errorf("jitter desc: got %v, want %v", got, want)
	}
}

func TestSortIDsNoneIsPassthrough(t *testing.T) {
	updates := make(chan pinger.StatsUpdate)
	m := New([]string{"b", "a", "c"}, updates, false, false)
	m.stats["a"] = pinger.StatsUpdate{RTT: 1 * time.Millisecond, Sent: 1, Recv: 1}
	m.stats["b"] = pinger.StatsUpdate{RTT: 99 * time.Millisecond, Sent: 1, Recv: 1}
	m.stats["c"] = pinger.StatsUpdate{RTT: 50 * time.Millisecond, Sent: 1, Recv: 1}

	got := m.visibleIDs()
	want := []string{"b", "a", "c"}
	if !equalSlice(got, want) {
		t.Errorf("sortCol=-1 should be passthrough: got %v, want %v", got, want)
	}
}

func TestSortCycleVisitsAllSortableColumns(t *testing.T) {
	updates := make(chan pinger.StatsUpdate)
	m := New([]string{"a"}, updates, false, false)
	// termWidth=0 means visibleColumns returns all columns, so the cycle
	// should walk every sortable column index then wrap to -1.
	want := []int{0, 1, 2, 3, 4, 5, 6, -1, 0}
	for i, w := range want {
		mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
		m = mm.(Model)
		if m.sortCol != w {
			t.Errorf("press %d: sortCol=%d, want %d", i+1, m.sortCol, w)
		}
	}
}

func TestSortCycleBackwardsThroughAllColumns(t *testing.T) {
	updates := make(chan pinger.StatsUpdate)
	m := New([]string{"a"}, updates, false, false)
	// From -1, S should jump to the last sortable column and walk back.
	want := []int{6, 5, 4, 3, 2, 1, 0, -1, 6}
	for i, w := range want {
		mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'S'}})
		m = mm.(Model)
		if m.sortCol != w {
			t.Errorf("press %d: sortCol=%d, want %d", i+1, m.sortCol, w)
		}
	}
}

func TestSortCycleSkipsHiddenColumns(t *testing.T) {
	updates := make(chan pinger.StatsUpdate)
	m := New([]string{"a"}, updates, false, false)
	// At termWidth=70 the responsive layout hides MIN/AVG/MAX (tier 3)
	// AND SENT/LOST (tier 2). Remaining sortable visible columns are
	// TARGET(0), RTT(1), JITTER(5), LOSS%(6). SPARK is visible but not
	// sortable and should be skipped.
	m.termWidth = 70
	want := []int{0, 1, 5, 6, -1, 0}
	for i, w := range want {
		mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
		m = mm.(Model)
		if m.sortCol != w {
			t.Errorf("press %d: sortCol=%d, want %d", i+1, m.sortCol, w)
		}
	}
}

func TestSortRevertsOnResizeHidingColumn(t *testing.T) {
	updates := make(chan pinger.StatsUpdate)
	m := New([]string{"a"}, updates, false, false)
	// Sort on MIN (col 2, tier 3), then resize narrow enough to hide
	// tier-3 columns. Sort should clear.
	m.sortCol = 2
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 70, Height: 24})
	m = mm.(Model)
	if m.sortCol != -1 {
		t.Errorf("hiding active sort column should revert sortCol to -1, got %d", m.sortCol)
	}
}

func TestSortSurvivesResizeKeepingColumn(t *testing.T) {
	updates := make(chan pinger.StatsUpdate)
	m := New([]string{"a"}, updates, false, false)
	// Sort on RTT (col 1, tier 0 — always visible). Even a narrow resize
	// should keep the sort active.
	m.sortCol = 1
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
	m = mm.(Model)
	if m.sortCol != 1 {
		t.Errorf("tier-0 sort should survive resize, got sortCol=%d", m.sortCol)
	}
}

func TestSortDirToggle(t *testing.T) {
	updates := make(chan pinger.StatsUpdate)
	m := New([]string{"a"}, updates, false, false)

	// Unsorted: r is a no-op (sortDesc stays at the New() default).
	startDesc := m.sortDesc
	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = mm.(Model)
	if m.sortDesc != startDesc {
		t.Errorf("r while unsorted should be no-op: sortDesc=%v, want %v", m.sortDesc, startDesc)
	}

	// Engage a sort, then r flips direction each press.
	m.sortCol = 1
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = mm.(Model)
	if m.sortDesc == startDesc {
		t.Errorf("r while sorted should flip sortDesc, got %v", m.sortDesc)
	}
	mm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = mm.(Model)
	if m.sortDesc != startDesc {
		t.Errorf("second r should flip back, got %v", m.sortDesc)
	}
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestVisibleColumns(t *testing.T) {
	// Headers in tier order so the assertions read naturally.
	const (
		full     = "TARGET,RTT,MIN,AVG,MAX,JITTER,LOSS%,SENT/LOST,SPARK"
		noMMM    = "TARGET,RTT,JITTER,LOSS%,SENT/LOST,SPARK"
		noSent   = "TARGET,RTT,JITTER,LOSS%,SPARK"
		noSpark  = "TARGET,RTT,JITTER,LOSS%"
	)
	for _, tc := range []struct {
		name      string
		termWidth int
		want      string
	}{
		{"unset: render all", 0, full},
		{"exactly fits all", 120, full},
		{"one shy of all: drop MIN/AVG/MAX", 119, noMMM},
		{"fits without MIN/AVG/MAX", 90, noMMM},
		{"one shy: drop SENT/LOST too", 89, noSent},
		{"fits without SENT/LOST", 78, noSent},
		{"one shy: drop SPARK too", 77, noSpark},
		{"fits at minimum", 56, noSpark},
		{"narrower than minimum: stay at 4", 30, noSpark},
	} {
		t.Run(tc.name, func(t *testing.T) {
			idx := visibleColumns(tc.termWidth)
			got := make([]string, len(idx))
			for i, ci := range idx {
				got[i] = columns[ci].header
			}
			if joined := strings.Join(got, ","); joined != tc.want {
				t.Errorf("termWidth=%d:\n got=%q\nwant=%q", tc.termWidth, joined, tc.want)
			}
		})
	}
}
