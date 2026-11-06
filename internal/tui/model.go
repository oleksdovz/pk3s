// Package tui implements the interactive terminal UI on top of app.
package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/oleksdovz/pk3s/internal/app"
	"github.com/oleksdovz/pk3s/internal/engine"
	"github.com/oleksdovz/pk3s/internal/plan"
	"github.com/oleksdovz/pk3s/internal/pve"
	"github.com/oleksdovz/pk3s/internal/report"
	"github.com/oleksdovz/pk3s/internal/spec"
	"github.com/oleksdovz/pk3s/internal/state"
	"github.com/oleksdovz/pk3s/internal/validate"
)

// Backend is what the TUI needs from app.App (an interface for tests).
type Backend interface {
	Inventory(ctx context.Context) (*pve.Inventory, error)
	Prepare(ctx context.Context, s spec.ClusterSpec, inv *pve.Inventory) (*app.Prepared, error)
	Clusters() ([]string, error)
	Status(ctx context.Context, name string) (*app.StatusReport, error)
	LoadForResume(ctx context.Context, name string) (*plan.Plan, error)
	NewSession(p *plan.Plan, resume bool, notify func(engine.Event)) (*app.Session, error)
	LoadSpec(name string) (spec.ClusterSpec, error)
	ExportSpec(ctx context.Context, name string) (string, error)
	PlanDestroy(ctx context.Context, name string) (*app.DestroyPlan, error)
	Destroy(ctx context.Context, dp *app.DestroyPlan, progress func(string)) error
	Logf(format string, args ...any)
	StateRoot() string
}

type screen int

const (
	scrLoading screen = iota
	scrError
	scrMenu
	scrWizard
	scrPreparing
	scrReview
	scrConfirm
	scrRunning
	scrDone
	scrStatus
	scrDestroy
)

// Messages.
type (
	invMsg struct {
		inv *pve.Inventory
		err error
	}
	preparedMsg struct {
		prep *app.Prepared
		err  error
	}
	resumeMsg struct {
		p   *plan.Plan
		err error
	}
	statusMsg struct {
		text string
		err  error
	}
	eventMsg   engine.Event
	runDoneMsg struct{ err error }
)

// Model is the root Bubble Tea model.
type Model struct {
	ctx     context.Context
	be      Backend
	version string

	width, height int
	screen        screen
	err           error
	flash         string

	inv  *pve.Inventory
	spec spec.ClusterSpec

	// wizard
	sections []section
	secIdx   int
	fieldIdx int
	focus    int // 0 = sections, 1 = fields
	editing  bool
	input    textinput.Model
	fieldErr string
	findings validate.Findings

	// menu
	clusters []string
	menuIdx  int

	// review / confirm
	prepared *app.Prepared
	plan     *plan.Plan
	resume   bool
	view     viewport.Model
	confirm  textinput.Model

	// run
	spin       spinner.Model
	session    *app.Session
	events     chan tea.Msg
	cancelRun  context.CancelFunc
	stopAsked  bool
	stepStatus map[string]string
	current    string
	logs       []string
	runErr     error

	// status
	statusName string
	statusText string

	// navigation
	help      bool
	reviewSel bool // "Review plan & deploy" entry selected in the page list

	// destroy
	destroy     *app.DestroyPlan
	destroyLog  []string
	destroyErr  error
	destroying  bool
	destroyDone bool
}

// New creates the model.
func New(ctx context.Context, be Backend, version string) Model {
	ti := textinput.New()
	ti.CharLimit = 256
	ci := textinput.New()
	ci.Placeholder = "yes"
	sp := spinner.New()
	sp.Spinner = spinner.Line
	return Model{
		ctx: ctx, be: be, version: version, screen: scrLoading,
		input: ti, confirm: ci, spin: sp, view: viewport.New(80, 20),
		stepStatus: map[string]string{},
	}
}

// Run starts the TUI.
func Run(ctx context.Context, a *app.App, version string) error {
	p := tea.NewProgram(New(ctx, a, version), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, m.loadInventory())
}

func (m Model) loadInventory() tea.Cmd {
	return func() tea.Msg {
		inv, err := m.be.Inventory(m.ctx)
		return invMsg{inv, err}
	}
}

// --- live validation -------------------------------------------------------

// placeholderVersion lets the wizard build a plan without network access.
const placeholderVersion = "v0.0.0+k3s1"

func (m *Model) revalidate() {
	m.sections = buildSections(&m.spec, m.inv)
	if m.secIdx >= len(m.sections) {
		m.secIdx = len(m.sections) - 1
	}
	if n := len(m.sections[m.secIdx].fields); m.fieldIdx >= n {
		m.fieldIdx = max(0, n-1)
	}
	f := validate.Spec(m.spec, m.inv)
	if p, err := plan.Build(m.spec, m.inv, placeholderVersion); err == nil {
		f = append(f, validate.Plan(p, m.inv, validate.Probes{})...)
	} else {
		f = append(f, validate.Finding{Severity: validate.Error, Field: "network.first_ip", Message: err.Error()})
	}
	m.findings = f
}

func (m *Model) sectionFindings(i int) validate.Findings {
	var out validate.Findings
	for _, p := range m.sections[i].prefixes {
		out = append(out, m.findings.ForField(p)...)
	}
	return out
}

// --- update ---------------------------------------------------------------

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.view.Width = max(20, msg.Width-4)
		m.view.Height = max(5, msg.Height-6)
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case invMsg:
		if msg.err != nil {
			m.screen, m.err = scrError, fmt.Errorf("reading Proxmox inventory: %w", msg.err)
			return m, nil
		}
		m.inv = msg.inv
		m.clusters, _ = m.be.Clusters()
		m.screen = scrMenu
		return m, nil
	case preparedMsg:
		return m.onPrepared(msg)
	case resumeMsg:
		if msg.err != nil {
			m.flash = "resume: " + msg.err.Error()
			return m, nil
		}
		m.plan, m.resume = msg.p, true
		m.screen = scrConfirm
		m.confirm.SetValue("")
		m.confirm.Focus()
		return m, nil
	case statusMsg:
		if msg.err != nil {
			m.statusText = "Error: " + msg.err.Error()
		} else {
			m.statusText = msg.text
		}
		return m, nil
	case eventMsg:
		m.onEvent(engine.Event(msg))
		return m, m.waitEvent()
	case runDoneMsg:
		m.runErr = msg.err
		if m.session != nil {
			m.session.Close()
		}
		m.screen = scrDone
		m.clusters, _ = m.be.Clusters()
		return m, nil
	case destroyPlanMsg:
		return m.onDestroyPlan(msg)
	case destroyDoneMsg:
		m.destroying, m.destroyDone = false, true
		m.destroyLog, m.destroyErr = msg.log, msg.err
		m.clusters, _ = m.be.Clusters()
		return m, nil
	case exportMsg:
		if msg.err != nil {
			m.flash = "export: " + msg.err.Error()
		} else {
			m.flash = "Deployed spec written to " + msg.path + "  (edit it and run: pk3s apply -f " + msg.path + ")"
		}
		return m, nil
	case tea.KeyMsg:
		return m.onKey(msg)
	}
	return m, nil
}

func (m Model) onKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if m.help {
		if key == "esc" || fkey(k) == fHelp || key == "enter" || key == "q" {
			m.help = false
		}
		return m, nil
	}
	if m.screen == scrRunning {
		if key == "ctrl+c" {
			return m.requestStop()
		}
		if n := fkey(k); n > 0 {
			return m.onFKey(n)
		}
		return m, nil
	}
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	if n := fkey(k); n > 0 && !(m.screen == scrWizard && m.editing) {
		m.flash = ""
		return m.onFKey(n)
	}
	m.flash = ""
	switch m.screen {
	case scrError:
		if key == "q" || key == "enter" || key == "esc" {
			return m, tea.Quit
		}
	case scrMenu:
		return m.keyMenu(key)
	case scrWizard:
		return m.keyWizard(k)
	case scrReview:
		return m.keyReview(k)
	case scrConfirm:
		return m.keyConfirm(k)
	case scrDestroy:
		return m.keyDestroy(k)
	case scrDone:
		if key == "enter" || key == "esc" {
			m.screen = scrMenu
		} else if key == "q" {
			return m, tea.Quit
		}
	case scrStatus:
		switch key {
		case "esc", "backspace":
			m.screen = scrMenu
		case "r":
			name := m.statusName
			return m, func() tea.Msg {
				p, err := m.be.LoadForResume(m.ctx, name)
				return resumeMsg{p, err}
			}
		case "s":
			return m, m.loadStatus(m.statusName)
		case "e":
			return m.editCluster(m.statusName)
		}
	}
	return m, nil
}

func (m Model) keyMenu(key string) (tea.Model, tea.Cmd) {
	n := 1 + len(m.clusters)
	switch key {
	case "up", "k":
		m.menuIdx = (m.menuIdx + n - 1) % n
	case "down", "j":
		m.menuIdx = (m.menuIdx + 1) % n
	case "q":
		return m, tea.Quit
	case "enter":
		if m.menuIdx == 0 {
			return m.startNew()
		}
		m.statusName = m.clusters[m.menuIdx-1]
		m.statusText = ""
		m.screen = scrStatus
		return m, m.loadStatus(m.statusName)
	}
	return m, nil
}

func (m Model) loadStatus(name string) tea.Cmd {
	return func() tea.Msg {
		r, err := m.be.Status(m.ctx, name)
		if err != nil {
			return statusMsg{err: err}
		}
		return statusMsg{text: report.Status(r)}
	}
}

func (m Model) currentField() *field {
	fs := m.sections[m.secIdx].fields
	if len(fs) == 0 {
		return nil
	}
	return &fs[m.fieldIdx]
}

func (m Model) keyWizard(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if m.editing {
		switch key {
		case "enter":
			f := m.currentField()
			if err := f.set(&m.spec, m.input.Value()); err != nil {
				m.fieldErr = err.Error()
				return m, nil
			}
			m.be.Logf("tui [%s] %s / %s = %q", m.spec.Name, m.sections[m.secIdx].title, f.label, f.get(&m.spec))
			m.editing, m.fieldErr = false, ""
			m.input.Blur()
			m.revalidate()
		case "esc":
			m.editing, m.fieldErr = false, ""
			m.input.Blur()
		default:
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(k)
			return m, cmd
		}
		return m, nil
	}

	f := m.currentField()
	switch key {
	case "tab", "shift+tab":
		m.focus = 1 - m.focus
		if m.focus == 1 {
			m.reviewSel = false
		}
	case "esc":
		m.screen = scrMenu
	case "ctrl+r":
		return m.startPlan()
	case "ctrl+s":
		m.flash = m.saveSpec()
	case "up", "k":
		if m.focus == 0 && m.reviewSel {
			m.reviewSel = false
		} else if m.focus == 0 {
			if m.secIdx == 0 {
				m.reviewSel = true
			} else {
				m.secIdx--
			}
			m.fieldIdx = 0
		} else if m.fieldIdx > 0 {
			m.fieldIdx--
		}
	case "down", "j":
		if m.focus == 0 && m.reviewSel {
			m.reviewSel, m.secIdx = false, 0
		} else if m.focus == 0 {
			if m.secIdx == len(m.sections)-1 {
				m.reviewSel = true
			} else {
				m.secIdx++
			}
			m.fieldIdx = 0
		} else if m.fieldIdx < len(m.sections[m.secIdx].fields)-1 {
			m.fieldIdx++
		}
	case "pgdown":
		m.secIdx = (m.secIdx + 1) % len(m.sections)
		m.fieldIdx = 0
	case "pgup":
		m.secIdx = (m.secIdx + len(m.sections) - 1) % len(m.sections)
		m.fieldIdx = 0
	case "left", "h":
		if m.focus == 1 && f != nil && f.kind == kindChoice && len(f.options) > 0 {
			m.cycle(f, -1)
		} else {
			m.focus = 0
		}
	case "right", "l":
		if m.focus == 1 && f != nil && f.kind == kindChoice && len(f.options) > 0 {
			m.cycle(f, 1)
		} else {
			m.focus = 1
		}
	case "enter", " ":
		if m.focus == 0 && m.reviewSel {
			return m.startPlan()
		}
		if m.focus == 0 {
			m.focus = 1
			return m, nil
		}
		if f == nil {
			return m, nil
		}
		switch {
		case f.kind == kindToggle:
			_ = f.set(&m.spec, yesNo(f.get(&m.spec) != "yes"))
			m.be.Logf("tui [%s] %s / %s = %s", m.spec.Name, m.sections[m.secIdx].title, f.label, f.get(&m.spec))
			m.revalidate()
		case f.kind == kindChoice && !f.custom && key == "enter":
			m.cycle(f, 1)
		case key == "enter":
			m.editing = true
			m.input.SetValue(f.get(&m.spec))
			m.input.CursorEnd()
			m.input.Focus()
			return m, textinput.Blink
		}
	}
	return m, nil
}

func (m *Model) cycle(f *field, dir int) {
	cur := f.get(&m.spec)
	idx := -1
	for i, o := range f.options {
		if o == cur {
			idx = i
		}
	}
	idx = (idx + dir + len(f.options)) % len(f.options)
	_ = f.set(&m.spec, f.options[idx])
	m.be.Logf("tui [%s] %s = %s", m.spec.Name, f.label, f.options[idx])
	m.revalidate()
}

func (m Model) saveSpec() string {
	name := m.spec.Name
	if name == "" {
		name = "cluster"
	}
	path := name + ".yaml"
	if err := spec.Save(path, m.spec); err != nil {
		return "save failed: " + err.Error()
	}
	m.be.Logf("tui: spec of %q saved to %s", m.spec.Name, path)
	wd, _ := os.Getwd()
	return "Saved " + wd + "/" + path + "  (use: pk3s apply -f " + path + ")"
}

func (m Model) onPrepared(msg preparedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.screen = scrWizard
		m.flash = "Planning failed: " + msg.err.Error()
		return m, nil
	}
	m.prepared = msg.prep
	m.plan = msg.prep.Plan
	m.resume = false
	m.screen = scrReview
	var b strings.Builder
	if m.plan != nil {
		b.WriteString(report.Plan(m.plan))
		b.WriteString("\n")
	}
	b.WriteString("Validation\n")
	b.WriteString(colorFindings(report.Findings(msg.prep.Findings)))
	if msg.prep.Findings.HasErrors() {
		b.WriteString("\n" + stErr.Render("Fix the errors above (Esc returns to the wizard). Nothing was changed.") + "\n")
	} else {
		b.WriteString("\n" + stOK.Render("Ready. Press Enter (or F9) to deploy, Esc to edit.") + "\n")
	}
	m.view.SetContent(b.String())
	m.view.GotoTop()
	return m, nil
}

func colorFindings(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		switch {
		case strings.HasPrefix(l, "ERROR"):
			l = stErr.Render(l)
		case strings.HasPrefix(l, "WARN"):
			l = stWarn.Render(l)
		case strings.HasPrefix(l, "INFO"):
			l = stDim.Render(l)
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func (m Model) keyReview(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "backspace":
		m.screen = scrWizard
		return m, nil
	case "ctrl+s":
		m.flash = m.saveSpec()
		return m, nil
	case "enter":
		if m.prepared == nil || m.plan == nil || m.prepared.Findings.HasErrors() {
			m.flash = "The plan has errors; nothing can be deployed."
			return m, nil
		}
		if m.plan.Mode == plan.ModeUpdate && len(m.plan.Changes) == 0 {
			m.flash = "Nothing to change."
			return m, nil
		}
		m.screen = scrConfirm
		m.confirm.SetValue("")
		m.confirm.Focus()
		return m, textinput.Blink
	}
	var cmd tea.Cmd
	m.view, cmd = m.view.Update(k)
	return m, cmd
}

func (m Model) keyConfirm(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.confirm.Blur()
		if m.resume {
			m.screen = scrStatus
		} else {
			m.screen = scrReview
		}
		return m, nil
	case "enter":
		if strings.TrimSpace(m.confirm.Value()) != "yes" {
			m.flash = "Type yes to continue, or Esc to go back."
			return m, nil
		}
		m.confirm.Blur()
		m.be.Logf("tui: %s of %q confirmed (resume=%v)", m.plan.Mode, m.plan.Spec.Name, m.resume)
		return m.startRun()
	}
	var cmd tea.Cmd
	m.confirm, cmd = m.confirm.Update(k)
	return m, cmd
}

// --- running ---------------------------------------------------------------

func (m Model) startRun() (tea.Model, tea.Cmd) {
	events := make(chan tea.Msg, 256)
	sess, err := m.be.NewSession(m.plan, m.resume, func(ev engine.Event) { events <- eventMsg(ev) })
	if err != nil {
		m.flash = err.Error()
		return m, nil
	}
	runCtx, cancel := context.WithCancel(m.ctx)
	m.session, m.events, m.cancelRun = sess, events, cancel
	m.stopAsked, m.runErr, m.logs, m.current = false, nil, nil, ""
	m.stepStatus = map[string]string{}
	m.screen = scrRunning
	go func() {
		err := sess.Run(runCtx)
		events <- runDoneMsg{err}
	}()
	return m, tea.Batch(m.spin.Tick, m.waitEvent())
}

func (m Model) waitEvent() tea.Cmd {
	ch := m.events
	return func() tea.Msg { return <-ch }
}

func (m *Model) onEvent(ev engine.Event) {
	switch ev.Kind {
	case engine.EventStepStart:
		m.stepStatus[ev.StepID] = state.StatusRunning
		m.current = ev.StepID
	case engine.EventStepDone:
		m.stepStatus[ev.StepID] = state.StatusDone
	case engine.EventStepSkipped:
		m.stepStatus[ev.StepID] = "skipped"
	case engine.EventStepFailed:
		m.stepStatus[ev.StepID] = state.StatusFailed
	}
	m.logs = append(m.logs, engine.FormatEvent(ev))
	if len(m.logs) > 500 {
		m.logs = m.logs[len(m.logs)-500:]
	}
}
