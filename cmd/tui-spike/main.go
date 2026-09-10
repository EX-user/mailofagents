// tui-spike — boss 0910 directive probe: render the account rolling area
// with TUI-library widgets (bubbles viewport + lipgloss box) instead of
// the hand-rolled clamp/chunk logic. Static frames only — same artifact
// shape as -tui-screenshot, no event loop. NOT wired into the worker;
// this is the visual decision artifact for boss.
package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wrap"
)

var (
	stateColor = map[string]lipgloss.Color{
		"waiting": lipgloss.Color("2"),
		"working": lipgloss.Color("6"),
		"compact": lipgloss.Color("3"),
		"error":   lipgloss.Color("1"),
	}
	statusStyle = lipgloss.NewStyle().Bold(true)
	boxStyle    = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("8")).
			Padding(0, 1)
)

func render(tag, state, detail string, roll string, logs []string, width int) string {
	var b strings.Builder
	st := statusStyle.Foreground(stateColor[state]).
		Render(fmt.Sprintf("[%s] %s", tag, strings.ToUpper(state)))
	line := fmt.Sprintf("%s · up 15m17s | %s | ctx ≈39k", st, detail)
	b.WriteString(line + "\n")

	// rolling area: library text box — content pre-wrapped with reflow
	// (CJK-width aware), viewport displays it in the box. Height 2 = the
	// two rolling rows.
	wrapped := wrap.String(roll, width-8) // box padding+borders eat 8 cols
	vp := viewport.New(width-4, 2)
	vp.SetContent(wrapped)
	b.WriteString(boxStyle.Render(vp.View()) + "\n")

	b.WriteString(statusStyle.Render("[worker-log]") + "\n")
	for _, l := range logs {
		b.WriteString("  " + l + "\n")
	}
	b.WriteString("  full logs: errors-*.log beside each account's state file")
	return b.String()
}

func main() {
	width := 100
	long := "这是一条超长内容。甲乙丙丁戊己庚辛壬癸ABCDEFGHIJK LMNOPQRSTUVWXYZ0123456789甲乙丙丁戊己庚辛壬癸（boss 原例：验证超长内容跨两行滚动换行，再补一段确保越过两行窗口触发硬截断的尾部省略号展示，这段再加长一些让窗口必截无疑尾部从此处起被省略）"
	fmt.Printf("=== spike frame A: viewport text box + border + CJK long content @ %d cols ===\n", width)
	fmt.Println(render("alpha", "working", "digest sent, model is on it…", long,
		[]string{"[alpha] wake failed: provider quota/429 · session salvaged"}, width))

	fmt.Printf("\n=== spike frame B: colored four states, box per account ===\n")
	now := time.Now()
	_ = now
	for _, s := range []string{"working", "waiting", "compact", "error"} {
		var b strings.Builder
		st := statusStyle.Foreground(stateColor[s]).
			Render(fmt.Sprintf("[alpha] %s", strings.ToUpper(s)))
		b.WriteString(fmt.Sprintf("%s · up 15m17s | ctx ≈39k", st) + "\n")
		vp := viewport.New(width-4, 2)
		vp.SetContent("tool · bash · go test ./internal/worker/")
		b.WriteString(boxStyle.Render(vp.View()))
		fmt.Println(b.String() + "\n")
	}
}
