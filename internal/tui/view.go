package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/oleksdovz/pk3s/internal/app"
	"github.com/oleksdovz/pk3s/internal/engine"
	"github.com/oleksdovz/pk3s/internal/plan"
	"github.com/oleksdovz/pk3s/internal/report"
	"github.com/oleksdovz/pk3s/internal/state"
	"github.com/oleksdovz/pk3s/internal/validate"
)

// chromeLines is header + flash + hint + function-key bar.
const chromeLines = 4

// screenSize returns the terminal size (with defaults before the first
// WindowSizeMsg).
func (m Model) screenSize() (int, int) {
	w, h := m.width, m.height
	if w <= 0 {
		w = 100
	}
	if h <= 0 {
		h = 30
	}
	return w, h
}

// bodyHeight is the number of lines available between header and footer.
func (m Model) bodyHeight() int {
	_, h := m.screenSize()
	return max(3, h-chromeLines)
}

// fit cuts or pads text to exactly h lines no wider than w, so the frame
// never exceeds the terminal (which would scroll and leave garbage).
func fit(s string, w, h int) string {
	lines := strings.Split(lipgloss.NewStyle().MaxWidth(w).Render(s), "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// View implements tea.Model.
func (m Model) View() string {
	w, _ := m.screenSize()
	var body, hint string
	switch m.screen {
	case scrLoading:
		body = "\n  " + m.spin.View() + " Reading Proxmox inventory (pvesh)..."
	case scrError:
		body = "\n  " + stErr.Render(m.err.Error()) + "\n\n  pk3s must run as root on a Proxmox VE host.\n  Press Enter to exit."
	case scrMenu:
		body = m.viewMenu()
		hint = "↑↓ select  Enter open"
	case scrWizard:
		body = m.viewWizard(w)
		if m.editing {
			hint = "Enter accept  Esc cancel"
		} else {
			hint = "Tab panel  ↑↓ move  PgUp/PgDn page  Enter edit  ←→ choose  Space toggle  Esc menu"
		}
	case scrPreparing:
		body = "\n  " + m.spin.View() + " Resolving k3s version, probing IP addresses and validating the plan..."
	case scrReview:
		title := "Deployment plan (nothing has been changed yet)"
		if m.canApply() {
			title += "   " + stSel.Render(" Enter / F9 = Deploy ")
		}
		body = stPanelFocus.Width(w - 2).Render(stTitle.Render(title) + "\n" + m.view.View())
		hint = "↑↓ PgUp PgDn scroll  Enter deploy  Esc back"
	case scrConfirm:
		body = m.viewConfirm()
		hint = "Enter confirm  Esc back"
	case scrRunning:
		body = m.viewRunning(w)
		hint = "F10 / Ctrl+C stop after the current step"
	case scrDone:
		body = m.viewDone(w)
		hint = "Enter main menu"
	case scrStatus:
		body = m.viewStatus(w)
		hint = "Esc back"
	case scrDestroy:
		body = m.viewDestroy(w)
		hint = "Enter confirm  Esc cancel"
	}
	if m.help {
		body = stModal.Width(min(w-4, 100)).Render(stTitle.Render("Help") + "\n\n" + m.helpText())
	}

	header := fit(stHeader.Width(w).Render(truncate(m.headerText(), w)), w, 1)
	flash := ""
	if m.flash != "" {
		flash = stWarn.Render(truncate(" "+m.flash, w))
	}
	return strings.Join([]string{
		header,
		fit(body, w, m.bodyHeight()),
		fit(flash, w, 1),
		fit(stDim.Render(truncate(" "+hint, w)), w, 1),
		fit(m.fkeyBar(w), w, 1),
	}, "\n")
}

func (m Model) headerText() string {
	s := " pk3s " + m.version
	if m.inv != nil {
		s += fmt.Sprintf(" │ node %s │ PVE %s │ kernel %s │ %s", m.inv.Node, m.inv.PVEVersion, m.inv.Kernel, m.inv.Arch)
	}
	return s
}

// fkeyBar renders the Midnight-Commander-style bar: ten equal slots,
// number then label; unavailable slots are dimmed.
func (m Model) fkeyBar(w int) string {
	labels := m.fkeyLabels()
	slot := max(3, w/10)
	var b strings.Builder
	for n := 1; n <= 10; n++ {
		num := fmt.Sprintf("%d", n)
		label := labels[n]
		width := slot - len(num)
		if n == 10 {
			width = max(1, w-9*slot-len(num))
		}
		if label == "" {
			b.WriteString(stDim.Render(num) + strings.Repeat(" ", max(0, width)))
			continue
		}
		b.WriteString(stKeyLabel.Render(num) + stKey.Render(padRight(truncate(label, width), width)))
	}
	return b.String()
}

func padRight(s string, n int) string {
	if r := n - lipgloss.Width(s); r > 0 {
		return s + strings.Repeat(" ", r)
	}
	return s
}

func (m Model) viewDestroy(w int) string {
	var b strings.Builder
	dp := m.destroy
	b.WriteString(stTitle.Render("Destroy cluster "+dp.Cluster) + "\n\n")
	for _, c := range dp.Containers {
		switch {
		case c.Exists && c.Owned:
			fmt.Fprintf(&b, "  CT %d: %s\n", c.CTID, stErr.Render("will be stopped and destroyed"))
		default:
			fmt.Fprintf(&b, "  CT %d: %s\n", c.CTID, c.Reason)
		}
	}
	if len(dp.Containers) == 0 {
		b.WriteString("  No containers recorded for this cluster.\n")
	}
	if len(dp.Kept) > 0 {
		b.WriteString("\nKept (never deleted automatically):\n")
		for _, r := range dp.Kept {
			fmt.Fprintf(&b, "  %s %s\n", r.Kind, r.ID)
		}
	}
	switch {
	case m.destroying:
		b.WriteString("\n" + m.spin.View() + " destroying...\n")
	case m.destroyDone:
		b.WriteString("\n")
		for _, l := range m.destroyLog {
			b.WriteString("  " + l + "\n")
		}
		if m.destroyErr != nil {
			b.WriteString(stErr.Render("Destroy failed: "+m.destroyErr.Error()) + "\n")
		} else {
			b.WriteString(stOK.Render("Cluster destroyed.") + "\n")
		}
		b.WriteString("\nPress Enter to return to the main menu.")
	case dp.Blocked():
		b.WriteString("\n" + stErr.Render("Blocked: a container no longer carries the cluster tag. Nothing will be destroyed.") + "\n")
	default:
		b.WriteString("\nType the cluster name to confirm: " + m.confirm.View())
	}
	return stModal.Width(min(w-4, 100)).Render(b.String())
}

func (m Model) viewMenu() string {
	var b strings.Builder
	b.WriteString(stTitle.Render("k3s clusters in LXC") + "\n\n")
	items := append([]string{"+ Create new cluster"}, m.clusters...)
	for i, it := range items {
		line := "  " + it
		if i > 0 {
			line = "  ◆ " + it
		}
		if i == m.menuIdx {
			line = stSel.Render(line + "  ")
		}
		b.WriteString(line + "\n")
	}
	if len(m.clusters) == 0 {
		b.WriteString("\n" + stDim.Render("  No clusters managed by pk3s yet."))
	}
	b.WriteString("\n" + stDim.Render("  State: "+m.be.StateRoot()+"/clusters"))
	return stPanelFocus.Render(b.String())
}

func sevMark(fs validate.Findings) string {
	sev := -1
	for _, f := range fs {
		if int(f.Severity) > sev {
			sev = int(f.Severity)
		}
	}
	switch validate.Severity(sev) {
	case validate.Error:
		return stErr.Render("✗")
	case validate.Warning:
		return stWarn.Render("!")
	default:
		return stOK.Render("✓")
	}
}

func (m Model) viewWizard(w int) string {
	// Left panel: pages.
	var left strings.Builder
	left.WriteString(stTitle.Render("Steps") + "\n")
	for i, s := range m.sections {
		line := fmt.Sprintf("%s %2d %s", sevMark(m.sectionFindings(i)), i+1, s.title)
		if i == m.secIdx {
			if m.focus == 0 {
				line = stSel.Render(fmt.Sprintf("  %2d %-18s", i+1, s.title))
			} else {
				line = stTitle.Render("▸") + line[1:]
			}
		}
		left.WriteString(line + "\n")
	}
	review := "▶ Review plan & deploy"
	switch {
	case m.reviewSel && m.focus == 0:
		review = stSel.Render(" " + review + "  ")
	case m.findings.HasErrors():
		review = stDim.Render(" " + review)
	default:
		review = stOK.Render(" " + review)
	}
	left.WriteString("\n" + review + "\n" + stDim.Render("  (F9 / Alt+9)"))
	leftStyle := stPanel
	if m.focus == 0 {
		leftStyle = stPanelFocus
	}
	leftBox := leftStyle.Width(26).Render(left.String())

	// Right panel: fields of the current page.
	sec := m.sections[m.secIdx]
	rw := max(40, w-lipgloss.Width(leftBox)-2)
	var right strings.Builder
	right.WriteString(stTitle.Render(sec.title) + "\n")
	if sec.intro != "" {
		right.WriteString(stDim.Render(sec.intro) + "\n")
	}
	right.WriteString("\n")

	avail := max(3, m.bodyHeight()-10-strings.Count(sec.intro, "\n"))
	start, end := window(len(sec.fields), m.fieldIdx, avail)
	if start > 0 {
		right.WriteString(stDim.Render("  ↑ more") + "\n")
	}
	for i := start; i < end; i++ {
		f := sec.fields[i]
		val := f.get(&m.spec)
		if m.editing && i == m.fieldIdx {
			val = m.input.View()
		} else {
			switch f.kind {
			case kindToggle:
				if val == "yes" {
					val = "[x]"
				} else {
					val = "[ ]"
				}
			case kindChoice:
				val = "‹ " + val + " ›"
			}
			if val == "" {
				val = stDim.Render("(default)")
			}
		}
		line := fmt.Sprintf("  %-28s %s", f.label, val)
		if i == m.fieldIdx && m.focus == 1 && !m.editing {
			line = stSel.Render(fmt.Sprintf("  %-28s %s ", f.label, f.get(&m.spec)))
		}
		right.WriteString(line + "\n")
	}
	if end < len(sec.fields) {
		right.WriteString(stDim.Render("  ↓ more") + "\n")
	}
	if f := m.currentField(); f != nil && f.help != "" {
		right.WriteString("\n" + stDim.Render("  "+f.help) + "\n")
	}
	if m.fieldErr != "" {
		right.WriteString(stErr.Render("  "+m.fieldErr) + "\n")
	}
	if fs := m.sectionFindings(m.secIdx); len(fs) > 0 {
		right.WriteString("\n")
		for _, f := range fs {
			right.WriteString(colorFindings(fmt.Sprintf("%-5s %s", f.Severity, f.Message)) + "\n")
		}
	}
	rightStyle := stPanel
	if m.focus == 1 {
		rightStyle = stPanelFocus
	}
	rightBox := rightStyle.Width(rw - 4).Render(right.String())
	return lipgloss.JoinHorizontal(lipgloss.Top, leftBox, rightBox)
}

// window returns [start,end) of n items keeping sel visible in size rows.
func window(n, sel, size int) (int, int) {
	if n <= size {
		return 0, n
	}
	start := max(0, sel-size/2)
	end := min(n, start+size)
	start = max(0, end-size)
	return start, end
}

func (m Model) viewConfirm() string {
	var b strings.Builder
	if m.resume {
		b.WriteString(stTitle.Render("Resume "+m.plan.Spec.Name) + "\n\n")
		b.WriteString("Completed steps are skipped; the failed step is retried.\n")
	} else if m.plan.Mode == plan.ModeUpdate {
		b.WriteString(stTitle.Render("Update running cluster "+m.plan.Spec.Name) + "\n\n")
		for _, c := range m.plan.Changes {
			b.WriteString("  * " + wrap(c, 76) + "\n")
		}
	} else {
		b.WriteString(stTitle.Render("Create cluster "+m.plan.Spec.Name) + "\n\n")
		fmt.Fprintf(&b, "This creates %d containers and applies the host changes listed in the plan.\n", len(m.plan.Nodes))
		fmt.Fprintf(&b, "Security profile: %s\n", m.plan.Profile.ID)
		b.WriteString(stWarn.Render(wrap(m.plan.Profile.Risk, 70)) + "\n")
	}
	b.WriteString("\nType yes to continue: " + m.confirm.View())
	return stModal.Render(b.String())
}

func wrap(s string, width int) string {
	var lines []string
	line := ""
	for _, w := range strings.Fields(s) {
		if len(line)+len(w)+1 > width && line != "" {
			lines = append(lines, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += w
	}
	if line != "" {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (m Model) stepIcon(id string) string {
	switch m.stepStatus[id] {
	case state.StatusDone:
		return stOK.Render("✓")
	case "skipped":
		return stDim.Render("·")
	case state.StatusFailed:
		return stErr.Render("✗")
	case state.StatusRunning:
		return stTitle.Render(m.spin.View())
	default:
		return " "
	}
}

func (m Model) viewRunning(w int) string {
	steps := m.plan.Steps
	cur := 0
	for i, s := range steps {
		if s.ID == m.current {
			cur = i
		}
	}
	rows := max(1, m.bodyHeight()-3) // panel border + title
	start, end := window(len(steps), cur, rows)
	var left strings.Builder
	left.WriteString(stTitle.Render(fmt.Sprintf("Steps (%d)", len(steps))) + "\n")
	for i := start; i < end; i++ {
		left.WriteString(m.stepIcon(steps[i].ID) + " " + truncate(steps[i].Title, 40) + "\n")
	}
	leftBox := stPanel.Width(46).Render(left.String())

	rw := max(30, w-lipgloss.Width(leftBox)-4)
	var right strings.Builder
	right.WriteString(stTitle.Render("Log") + "\n")
	logs := m.logs
	if len(logs) > rows {
		logs = logs[len(logs)-rows:]
	}
	for _, l := range logs {
		right.WriteString(truncate(l, rw-2) + "\n")
	}
	rightBox := stPanelFocus.Width(rw).Render(right.String())
	return lipgloss.JoinHorizontal(lipgloss.Top, leftBox, rightBox)
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if n > 1 && len([]rune(s)) > n {
		return string([]rune(s)[:n-1]) + "…"
	}
	return s
}

func (m Model) viewDone(w int) string {
	var b strings.Builder
	if m.runErr == nil {
		if m.plan.Mode == plan.ModeUpdate {
			b.WriteString(stOK.Render("Update finished") + "\n\n")
			for _, c := range m.plan.Changes {
				b.WriteString("  ✓ " + c + "\n")
			}
		} else {
			b.WriteString(stOK.Render("Deployment finished") + "\n\n")
			b.WriteString(report.Summary(m.plan, m.session.Store.Path("kubeconfig")))
		}
	} else {
		if m.runErr == engine.ErrStopped {
			b.WriteString(stWarn.Render("Deployment stopped by user") + "\n\n")
		} else {
			b.WriteString(stErr.Render("Deployment failed") + "\n\n")
			b.WriteString(m.runErr.Error() + "\n\n")
		}
		b.WriteString(app.RecoveryHint(m.plan.Spec.Name, m.be.StateRoot()) + "\n")
		b.WriteString("\nResume from the main menu (select the cluster, press r).\n")
	}
	return stPanelFocus.Width(w - 2).Render(b.String())
}

func (m Model) viewStatus(w int) string {
	text := m.statusText
	if text == "" {
		text = m.spin.View() + " loading..."
	}
	text += "\n" + stDim.Render("Remove containers created by pk3s: pk3s destroy "+m.statusName+" (asks for confirmation)")
	return stPanelFocus.Width(w - 2).Render(stTitle.Render("Cluster "+m.statusName) + "\n\n" + text)
}
