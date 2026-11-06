package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/oleksdovz/pk3s/internal/app"
	"github.com/oleksdovz/pk3s/internal/spec"
)

// Function keys (Midnight Commander style). Every screen uses the same
// slot for the same verb; unavailable slots are shown dimmed.
const (
	fHelp    = 1
	fSave    = 2
	fView    = 3
	fEdit    = 4
	fNew     = 5
	fResume  = 6
	fExport  = 7
	fDestroy = 8
	fPlan    = 9 // Plan in the wizard, Apply on the plan review
	fQuit    = 10
)

// fkey maps F1..F10 (and Alt+1..Alt+0 for terminals that capture F-keys)
// to 1..10; 0 for other keys.
func fkey(k tea.KeyMsg) int {
	s := k.String()
	switch {
	case strings.HasPrefix(s, "f") && len(s) <= 3:
		var n int
		for _, c := range s[1:] {
			if c < '0' || c > '9' {
				return 0
			}
			n = n*10 + int(c-'0')
		}
		if n >= 1 && n <= 10 {
			return n
		}
	case strings.HasPrefix(s, "alt+") && len(s) == 5 && s[4] >= '0' && s[4] <= '9':
		if s[4] == '0' {
			return 10
		}
		return int(s[4] - '0')
	}
	return 0
}

// selectedCluster returns the cluster the menu cursor or status screen
// refers to ("" for "Create new cluster").
func (m Model) selectedCluster() string {
	switch m.screen {
	case scrStatus, scrDone:
		return m.statusName
	case scrMenu:
		if m.menuIdx > 0 && m.menuIdx <= len(m.clusters) {
			return m.clusters[m.menuIdx-1]
		}
	}
	return ""
}

// fkeyLabels returns the label of each F-key on the current screen; an
// empty label means the key does nothing here.
func (m Model) fkeyLabels() [11]string {
	var l [11]string
	l[fHelp] = "Help"
	l[fQuit] = "Quit"
	cluster := m.selectedCluster() != ""
	switch m.screen {
	case scrMenu:
		l[fNew] = "New"
		if cluster {
			l[fView], l[fEdit], l[fResume], l[fExport], l[fDestroy] = "View", "Edit", "Resume", "Export", "Destroy"
		}
	case scrStatus:
		l[fView], l[fEdit], l[fResume], l[fExport], l[fDestroy] = "Refresh", "Edit", "Resume", "Export", "Destroy"
	case scrWizard:
		if m.editing {
			l[fHelp], l[fQuit] = "", ""
			break
		}
		l[fSave], l[fEdit], l[fPlan] = "Save", "Edit", "Plan"
	case scrReview:
		l[fSave], l[fEdit] = "Save", "Edit"
		if m.canApply() {
			l[fPlan] = "Apply"
		}
	case scrRunning:
		l[fQuit] = "Stop"
	case scrDone:
		if m.statusName != "" {
			l[fView] = "View"
		}
	case scrDestroy:
		l[fQuit] = "Cancel"
	}
	return l
}

func (m Model) canApply() bool {
	if m.prepared == nil || m.plan == nil || m.prepared.Findings.HasErrors() {
		return false
	}
	return !(m.plan.Mode == "update" && len(m.plan.Changes) == 0)
}

// onFKey runs the verb bound to an F-key on the current screen.
func (m Model) onFKey(n int) (tea.Model, tea.Cmd) {
	if m.fkeyLabels()[n] == "" {
		return m, nil
	}
	name := m.selectedCluster()
	switch n {
	case fHelp:
		m.help = !m.help
	case fQuit:
		if m.screen == scrRunning {
			return m.requestStop()
		}
		if m.screen == scrDestroy {
			m.screen = scrStatus
			return m, nil
		}
		return m, tea.Quit
	case fNew:
		return m.startNew()
	case fView:
		m.statusName, m.statusText, m.screen = name, "", scrStatus
		return m, m.loadStatus(name)
	case fEdit:
		switch m.screen {
		case scrWizard:
			return m.keyWizard(tea.KeyMsg{Type: tea.KeyEnter})
		case scrReview:
			m.screen = scrWizard
			return m, nil
		}
		return m.editCluster(name)
	case fResume:
		return m, func() tea.Msg {
			p, err := m.be.LoadForResume(m.ctx, name)
			return resumeMsg{p, err}
		}
	case fExport:
		return m, func() tea.Msg {
			path, err := m.be.ExportSpec(m.ctx, name)
			return exportMsg{path, err}
		}
	case fDestroy:
		m.statusName = name
		return m, func() tea.Msg {
			dp, err := m.be.PlanDestroy(m.ctx, name)
			return destroyPlanMsg{dp, err}
		}
	case fSave:
		m.flash = m.saveSpec()
	case fPlan:
		if m.screen == scrWizard {
			return m.startPlan()
		}
		return m.keyReview(tea.KeyMsg{Type: tea.KeyEnter})
	}
	return m, nil
}

func (m Model) startNew() (tea.Model, tea.Cmd) {
	m.spec = spec.Default()
	m.spec.Name = ""
	m.editName, m.layout = "", nil
	app.ApplyInventoryDefaults(&m.spec, m.inv)
	m.secIdx, m.fieldIdx, m.focus = 0, 0, 1
	m.revalidate()
	m.screen = scrWizard
	m.flash = "New cluster: enter a name first."
	m.be.Logf("tui: new cluster wizard")
	return m, nil
}

func (m Model) editCluster(name string) (tea.Model, tea.Cmd) {
	s, err := m.be.LoadSpec(name)
	if err != nil {
		m.flash = err.Error()
		return m, nil
	}
	in, err := m.be.UpdateInput(name, s)
	if err != nil {
		m.flash = err.Error()
		return m, nil
	}
	m.spec, m.editName, m.layout = s, name, in.Layout
	m.secIdx, m.fieldIdx, m.focus = 0, 0, 1
	m.revalidate()
	m.screen = scrWizard
	m.be.Logf("tui: editing deployed cluster %q", name)
	m.flash = "Editing deployed cluster " + name + ": change agents, servers, resources or k3s version, then F9."
	return m, nil
}

func (m Model) startPlan() (tea.Model, tea.Cmd) {
	m.screen = scrPreparing
	s, inv := m.spec, m.inv
	return m, tea.Batch(m.spin.Tick, func() tea.Msg {
		prep, err := m.be.Prepare(m.ctx, s, inv)
		return preparedMsg{prep, err}
	})
}

func (m Model) requestStop() (tea.Model, tea.Cmd) {
	if !m.stopAsked {
		m.stopAsked = true
		m.session.Engine.RequestStop()
		m.flash = "Stopping after the current step. Press F10 or Ctrl+C again to abort it now."
	} else if m.cancelRun != nil {
		m.cancelRun()
	}
	return m, nil
}

// --- destroy ----------------------------------------------------------------

type (
	destroyPlanMsg struct {
		dp  *app.DestroyPlan
		err error
	}
	destroyDoneMsg struct {
		log []string
		err error
	}
	exportMsg struct {
		path string
		err  error
	}
)

func (m Model) onDestroyPlan(msg destroyPlanMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.flash = "destroy: " + msg.err.Error()
		return m, nil
	}
	m.destroy, m.destroyLog, m.destroyErr, m.destroyDone = msg.dp, nil, nil, false
	m.screen = scrDestroy
	m.confirm.SetValue("")
	m.confirm.Placeholder = msg.dp.Cluster
	m.confirm.Focus()
	return m, textinput.Blink
}

func (m Model) keyDestroy(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.destroyDone || m.destroying {
		if k.String() == "enter" || k.String() == "esc" {
			m.clusters, _ = m.be.Clusters()
			m.menuIdx = 0
			m.screen = scrMenu
		}
		return m, nil
	}
	switch k.String() {
	case "esc":
		m.confirm.Blur()
		m.screen = scrStatus
		return m, nil
	case "enter":
		if m.destroy.Blocked() {
			m.flash = "Destroy is blocked: a container failed the ownership check."
			return m, nil
		}
		if strings.TrimSpace(m.confirm.Value()) != m.destroy.Cluster {
			m.flash = "Type the cluster name exactly to confirm, or Esc to cancel."
			return m, nil
		}
		m.confirm.Blur()
		m.destroying = true
		dp := m.destroy
		return m, tea.Batch(m.spin.Tick, func() tea.Msg {
			var log []string
			err := m.be.Destroy(m.ctx, dp, func(s string) { log = append(log, s) })
			return destroyDoneMsg{log, err}
		})
	}
	var cmd tea.Cmd
	m.confirm, cmd = m.confirm.Update(k)
	return m, cmd
}

// helpText describes the current screen.
func (m Model) helpText() string {
	common := `Function keys work the same on every screen; dimmed keys do nothing here.
If your terminal captures F-keys, use Alt+1 .. Alt+0 instead.
Ctrl+L redraws the screen at the current terminal size.

  F1 Help     F2 Save spec   F3 View status   F4 Edit       F5 New cluster
  F6 Resume   F7 Export spec F8 Destroy       F9 Plan/Apply F10 Quit (Stop while running)
`
	var s string
	switch m.screen {
	case scrMenu:
		s = `Main menu: clusters managed by pk3s on this host.
  ↑↓ select   Enter / F3 view status   F4 edit (scale, upgrade, resize)
  F5 create a new cluster   F6 resume an interrupted operation
  F7 write the deployed spec to <name>.yaml   F8 destroy (asks for the name)`
	case scrWizard:
		s = `Wizard: pages on the left, fields on the right.
  Tab switch panel   ↑↓ move   PgUp/PgDn page   Enter / F4 edit field
  ←→ choose from a list   Space toggle   Esc main menu
  F2 save the spec as <name>.yaml   F9 validate and show the plan (changes nothing)`
	case scrReview:
		s = `Plan review: nothing has been changed yet.
  ↑↓ PgUp PgDn scroll   F9 / Enter apply (asks you to type yes)
  F4 / Esc back to the wizard   F2 save the spec`
	case scrStatus:
		s = `Cluster status: containers and Kubernetes nodes.
  F3 refresh   F4 edit (scale, upgrade, resize)   F6 resume
  F7 export the deployed spec   F8 destroy (asks for the name)   Esc back`
	case scrRunning:
		s = `Deployment in progress. Progress is saved after every step.
  F10 / Ctrl+C stop after the current step (press again to abort the step);
  continue later with F6 Resume or 'pk3s resume <name>'.`
	case scrDestroy:
		s = `Destroy: only containers created by pk3s that still carry the cluster tag
are stopped and destroyed. Data directories and host files are kept.
Type the cluster name and press Enter, or Esc to cancel.`
	default:
		s = "Esc goes back."
	}
	return s + "\n\n" + common + "\nActivity log: " + app.DefaultLogDir + "/pk3s.log\nPress F1 or Esc to close."
}
