package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/oleksdovz/pk3s/internal/app"
	"github.com/oleksdovz/pk3s/internal/engine"
	"github.com/oleksdovz/pk3s/internal/report"
	"github.com/oleksdovz/pk3s/internal/state"
	"github.com/oleksdovz/pk3s/internal/validate"
)

// View implements tea.Model.
func (m Model) View() string {
	w := max(m.width, 60)
	var body string
	keys := []string{"10", "Quit"}
	switch m.screen {
	case scrLoading:
		body = "\n  " + m.spin.View() + " Reading Proxmox inventory (pvesh)..."
	case scrError:
		body = "\n  " + stErr.Render(m.err.Error()) + "\n\n  pk3s must run as root on a Proxmox VE host.\n  Press Enter to exit."
	case scrMenu:
		body = m.viewMenu()
		keys = []string{"Enter", "Open", "10", "Quit"}
	case scrWizard:
		body = m.viewWizard(w)
		if m.editing {
			keys = []string{"Enter", "Accept", "Esc", "Cancel"}
		} else {
			keys = []string{"Tab", "Panel", "Enter", "Edit", "←→", "Choose", "PgDn", "Next page", "2", "Save", "9", "Plan", "Esc", "Menu", "10", "Quit"}
		}
	case scrPreparing:
		body = "\n  " + m.spin.View() + " Resolving k3s version, probing IP addresses and validating the plan..."
	case scrReview:
		body = stPanelFocus.Width(w - 2).Render(stTitle.Render("Deployment plan (nothing has been changed yet)") + "\n" + m.view.View())
		keys = []string{"↑↓", "Scroll", "Enter", "Deploy", "2", "Save", "Esc", "Back", "10", "Quit"}
	case scrConfirm:
		body = m.viewConfirm()
		keys = []string{"Enter", "Confirm", "Esc", "Back"}
	case scrRunning:
		body = m.viewRunning(w)
		keys = []string{"^C", "Stop after step"}
	case scrDone:
		body = m.viewDone(w)
		keys = []string{"Enter", "Menu", "q", "Quit"}
	case scrStatus:
		body = m.viewStatus(w)
		keys = []string{"r", "Resume", "s", "Refresh", "Esc", "Back"}
	}

	header := stHeader.Width(w).Render(m.headerText())
	footer := m.keyBar(keys, w)
	flash := ""
	if m.flash != "" {
		flash = stWarn.Render(" " + m.flash)
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, flash, footer)
}

func (m Model) headerText() string {
	s := " pk3s " + m.version
	if m.inv != nil {
		s += fmt.Sprintf(" │ node %s │ PVE %s │ kernel %s │ %s", m.inv.Node, m.inv.PVEVersion, m.inv.Kernel, m.inv.Arch)
	}
	return s
}

func (m Model) keyBar(keys []string, w int) string {
	var parts []string
	for i := 0; i+1 < len(keys); i += 2 {
		parts = append(parts, stKey.Render(keys[i])+stKeyLabel.Render(keys[i+1]))
	}
	return lipgloss.NewStyle().Width(w).Render(strings.Join(parts, " "))
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
	left.WriteString("\n" + stDim.Render(" F9: review plan"))
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

	avail := max(5, m.height-14-strings.Count(sec.intro, "\n"))
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
		b.WriteString(stTitle.Render("Resume deployment of "+m.plan.Spec.Name) + "\n\n")
		b.WriteString("Completed steps are skipped; the failed step is retried.\n")
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
	rows := max(5, m.height-6)
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
		b.WriteString(stOK.Render("Deployment finished") + "\n\n")
		b.WriteString(report.Summary(m.plan, m.session.Store.Path("kubeconfig")))
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
