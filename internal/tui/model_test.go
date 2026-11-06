package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/oleksdovz/pk3s/internal/app"
	"github.com/oleksdovz/pk3s/internal/engine"
	"github.com/oleksdovz/pk3s/internal/plan"
	"github.com/oleksdovz/pk3s/internal/pve"
	"github.com/oleksdovz/pk3s/internal/spec"
	"github.com/oleksdovz/pk3s/internal/testutil"
	"github.com/oleksdovz/pk3s/internal/validate"
)

type fakeBackend struct {
	inv      *pve.Inventory
	prepared spec.ClusterSpec
}

func (f *fakeBackend) Inventory(context.Context) (*pve.Inventory, error) { return f.inv, nil }
func (f *fakeBackend) Prepare(_ context.Context, s spec.ClusterSpec, inv *pve.Inventory) (*app.Prepared, error) {
	f.prepared = s
	p, err := plan.Build(s, inv, "v1.37.2+k3s1")
	if err != nil {
		return nil, err
	}
	fs := append(validate.Spec(s, inv), validate.Plan(p, inv, validate.Probes{})...)
	return &app.Prepared{Plan: p, Findings: fs, Inv: inv}, nil
}
func (f *fakeBackend) Clusters() ([]string, error) { return nil, nil }
func (f *fakeBackend) Status(context.Context, string) (*app.StatusReport, error) {
	return nil, nil
}
func (f *fakeBackend) LoadForResume(context.Context, string) (*plan.Plan, error) { return nil, nil }
func (f *fakeBackend) NewSession(*plan.Plan, bool, func(engine.Event)) (*app.Session, error) {
	return nil, nil
}
func (f *fakeBackend) StateRoot() string                                  { return "/var/lib/pk3s" }
func (f *fakeBackend) LoadSpec(string) (spec.ClusterSpec, error)          { return spec.ClusterSpec{}, nil }
func (f *fakeBackend) ExportSpec(context.Context, string) (string, error) { return "/tmp/x.yaml", nil }
func (f *fakeBackend) PlanDestroy(context.Context, string) (*app.DestroyPlan, error) {
	return &app.DestroyPlan{Cluster: "lab"}, nil
}
func (f *fakeBackend) Destroy(context.Context, *app.DestroyPlan, func(string)) error { return nil }
func (f *fakeBackend) Logf(string, ...any)                                           {}

func send(t *testing.T, m Model, msgs ...tea.Msg) Model {
	t.Helper()
	for _, msg := range msgs {
		next, cmd := m.Update(msg)
		m = next.(Model)
		for _, out := range collect(cmd) {
			m = send(t, m, out)
		}
	}
	return m
}

// collect runs a command (and batched sub-commands) concurrently and
// returns the pk3s messages they produce. Timer-based commands (cursor
// blink, spinner) are abandoned after a short wait.
func collect(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	ch := make(chan []tea.Msg, 1)
	go func() {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			var out []tea.Msg
			for _, c := range batch {
				out = append(out, collect(c)...)
			}
			ch <- out
			return
		}
		switch msg.(type) {
		case invMsg, preparedMsg, statusMsg, resumeMsg, destroyPlanMsg, destroyDoneMsg, exportMsg:
			ch <- []tea.Msg{msg}
		default:
			ch <- nil
		}
	}()
	select {
	case out := <-ch:
		return out
	case <-time.After(50 * time.Millisecond):
		return nil
	}
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "pgdown":
		return tea.KeyMsg{Type: tea.KeyPgDown}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "f9":
		return tea.KeyMsg{Type: tea.KeyF9}
	case "f1":
		return tea.KeyMsg{Type: tea.KeyF1}
	case "f8":
		return tea.KeyMsg{Type: tea.KeyF8}
	case "f10":
		return tea.KeyMsg{Type: tea.KeyF10}
	case "alt+9":
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'9'}, Alt: true}
	case "ctrl+u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func typeText(s string) []tea.Msg {
	var out []tea.Msg
	for _, r := range s {
		out = append(out, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return out
}

func newModel(t *testing.T) (Model, *fakeBackend) {
	be := &fakeBackend{inv: testutil.HPInventory(t)}
	m := New(context.Background(), be, "test")
	m = send(t, m, tea.WindowSizeMsg{Width: 140, Height: 50}, invMsg{inv: be.inv})
	if m.screen != scrMenu {
		t.Fatalf("expected menu, got %v", m.screen)
	}
	return m, be
}

func TestWizardDefaultsFromInventory(t *testing.T) {
	m, _ := newModel(t)
	m = send(t, m, key("enter"))
	if m.screen != scrWizard {
		t.Fatal("expected wizard")
	}
	if m.spec.Network.Gateway != "192.168.0.1" || m.spec.Network.DNS[0] != "192.168.0.2" || m.spec.Network.SearchDomain != "lan" {
		t.Errorf("network defaults not taken from host: %+v", m.spec.Network)
	}
	if m.spec.RootfsStorage == "" || !strings.HasPrefix(m.spec.Template.Name, "debian-13") {
		t.Errorf("storage/template defaults: %q %q", m.spec.RootfsStorage, m.spec.Template.Name)
	}
	// First IP is empty, so the wizard must report a blocking error.
	if !m.findings.HasErrors() {
		t.Error("expected errors before first IP is set")
	}
	view := m.View()
	for _, want := range []string{"Cluster name", "Hostname prefix", "Node network", "Persistent data"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q", want)
		}
	}
}

// navigate to the page with the given title.
func gotoPage(t *testing.T, m Model, title string) Model {
	t.Helper()
	for i := 0; i < len(m.sections); i++ {
		if m.sections[m.secIdx].title == title {
			return m
		}
		m = send(t, m, key("pgdown"))
	}
	t.Fatalf("page %q not found", title)
	return m
}

func editField(t *testing.T, m Model, label, value string) Model {
	t.Helper()
	m.focus = 1
	for i, f := range m.sections[m.secIdx].fields {
		if f.label == label {
			m.fieldIdx = i
			m = send(t, m, key("enter"), key("ctrl+u"))
			m = send(t, m, typeText(value)...)
			return send(t, m, key("enter"))
		}
	}
	t.Fatalf("field %q not found", label)
	return m
}

func TestEditPrefixAndPerNodeNetworkThenPlan(t *testing.T) {
	m, be := newModel(t)
	m = send(t, m, key("enter"))
	m = editField(t, m, "Cluster name", "lab")
	m = editField(t, m, "Hostname prefix", "k3s-lab")
	m = gotoPage(t, m, "Network")
	m = editField(t, m, "First IP", "192.168.0.51")
	m = gotoPage(t, m, "Node network")
	m = editField(t, m, "k3s-lab-node2 IP", "192.168.0.77")
	m = editField(t, m, "k3s-lab-node2 DNS", "1.1.1.1, 9.9.9.9")
	m = gotoPage(t, m, "Security & host")
	m.focus = 1
	for i, f := range m.sections[m.secIdx].fields {
		if strings.HasPrefix(f.label, "I accept") {
			m.fieldIdx = i
		}
	}
	m = send(t, m, key(" "))
	if !m.spec.Security.AcknowledgePrivilegedRisk {
		t.Fatal("toggle did not set risk acknowledgement")
	}

	if m.findings.HasErrors() {
		t.Fatalf("unexpected errors: %v", m.findings)
	}
	m = send(t, m, key("f9"))
	if m.screen != scrReview {
		t.Fatalf("expected review, got %v (flash %q)", m.screen, m.flash)
	}
	o, ok := be.prepared.Override("node2")
	if !ok || o.IP != "192.168.0.77" || len(o.DNS) != 2 {
		t.Fatalf("override not passed to backend: %+v", be.prepared.NodeNetwork)
	}
	if !strings.Contains(m.view.View(), "k3s-lab-main1") {
		t.Error("plan view should list containers")
	}
	// Enter goes to the typed confirmation, not straight to deployment.
	m = send(t, m, key("enter"))
	if m.screen != scrConfirm {
		t.Fatal("expected confirmation screen")
	}
	m = send(t, m, typeText("no")...)
	m = send(t, m, key("enter"))
	if m.screen != scrConfirm {
		t.Fatal("wrong confirmation text must not start deployment")
	}
}

func TestInvalidNumberShowsFieldError(t *testing.T) {
	m, _ := newModel(t)
	m = send(t, m, key("enter"))
	m = gotoPage(t, m, "Topology")
	m.focus, m.fieldIdx = 1, 0
	m = send(t, m, key("enter"), key("ctrl+u"))
	m = send(t, m, typeText("three")...)
	m = send(t, m, key("enter"))
	if !m.editing || m.fieldErr == "" {
		t.Fatal("expected inline error and editing to continue")
	}
}

func TestClearingOverrideRestoresDefault(t *testing.T) {
	m, _ := newModel(t)
	m = send(t, m, key("enter"))
	m = editField(t, m, "Cluster name", "k3s")
	m = gotoPage(t, m, "Node network")
	m = editField(t, m, "k3s-node1 gateway", "192.168.0.254")
	if _, ok := m.spec.Override("node1"); !ok {
		t.Fatal("override not created")
	}
	m = editField(t, m, "k3s-node1 gateway", "")
	if _, ok := m.spec.Override("node1"); ok {
		t.Fatal("empty override should be removed")
	}
}

func TestFKeyBarAndHelp(t *testing.T) {
	m, _ := newModel(t)
	l := m.fkeyLabels()
	if l[fNew] != "New" || l[fQuit] != "Quit" || l[fDestroy] != "" {
		t.Fatalf("menu without a selected cluster: %v", l)
	}
	m.clusters, m.menuIdx = []string{"lab"}, 1
	l = m.fkeyLabels()
	if l[fView] != "View" || l[fEdit] != "Edit" || l[fDestroy] != "Destroy" || l[fExport] != "Export" {
		t.Fatalf("menu with a selected cluster: %v", l)
	}
	bar := m.fkeyBar(140)
	for _, want := range []string{"1", "Help", "8", "Destroy", "10", "Quit"} {
		if !strings.Contains(bar, want) {
			t.Errorf("bar missing %q: %s", want, bar)
		}
	}
	m = send(t, m, key("f1"))
	if !m.help || !strings.Contains(m.View(), "Main menu: clusters managed by pk3s") {
		t.Fatal("F1 should open help")
	}
	m = send(t, m, key("esc"))
	if m.help {
		t.Fatal("Esc should close help")
	}
}

func TestReviewEntryAndAltFallback(t *testing.T) {
	m, _ := newModel(t)
	m = send(t, m, key("enter"))
	m = editField(t, m, "Cluster name", "lab")
	m = gotoPage(t, m, "Network")
	m = editField(t, m, "First IP", "192.168.0.60")
	m = gotoPage(t, m, "Security & host")
	m.focus = 1
	for i, f := range m.sections[m.secIdx].fields {
		if strings.HasPrefix(f.label, "I accept") {
			m.fieldIdx = i
		}
	}
	m = send(t, m, key(" "))
	// The page list ends with "Review plan & deploy".
	m.focus = 0
	m = send(t, m, key("down"))
	if !m.reviewSel || !strings.Contains(m.View(), "Review plan & deploy") {
		t.Fatal("review entry not selected")
	}
	m = send(t, m, key("enter"))
	if m.screen != scrReview {
		t.Fatalf("Enter on the review entry should show the plan, got %v (%s)", m.screen, m.flash)
	}
	// Alt+9 works like F9 and leads to the typed confirmation.
	m = send(t, m, key("alt+9"))
	if m.screen != scrConfirm {
		t.Fatalf("Alt+9 should apply, got %v", m.screen)
	}
}

func TestDestroyNeedsExactName(t *testing.T) {
	m, _ := newModel(t)
	m.clusters, m.menuIdx = []string{"lab"}, 1
	m = send(t, m, key("f8"))
	if m.screen != scrDestroy {
		t.Fatalf("F8 should open destroy, got %v", m.screen)
	}
	m = send(t, m, typeText("la")...)
	m = send(t, m, key("enter"))
	if m.destroying || m.destroyDone {
		t.Fatal("wrong name must not destroy")
	}
	m = send(t, m, typeText("b")...)
	m = send(t, m, key("enter"))
	if !m.destroyDone || m.destroyErr != nil {
		t.Fatalf("destroy should run after typing the name: done=%v err=%v", m.destroyDone, m.destroyErr)
	}
}

// Every frame must fit the terminal exactly; a taller frame scrolls the
// alternate screen and leaves garbage after a resize.
func TestFrameFitsTerminalAfterResize(t *testing.T) {
	m, _ := newModel(t)
	m = send(t, m, key("enter")) // wizard
	m = editField(t, m, "Cluster name", "lab")
	m = gotoPage(t, m, "Node network")
	screens := map[string]Model{"wizard": m}

	r := m
	r.plan, _ = planFor(t)
	r.screen = scrRunning
	for _, st := range r.plan.Steps[:6] {
		r.stepStatus[st.ID] = "done"
		r.logs = append(r.logs, "[1/24] DONE  "+st.Title+" with a long tail to force truncation in the log panel")
	}
	screens["running"] = r

	for name, base := range screens {
		for _, size := range [][2]int{{140, 50}, {90, 30}, {60, 20}, {40, 12}} {
			mm := send(t, base, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			out := mm.View()
			lines := strings.Split(out, "\n")
			if len(lines) != size[1] {
				t.Errorf("%s %dx%d: %d lines", name, size[0], size[1], len(lines))
			}
			for i, l := range lines {
				if w := lipglossWidth(l); w > size[0] {
					t.Errorf("%s %dx%d: line %d is %d wide", name, size[0], size[1], i, w)
					break
				}
			}
		}
	}
}

func lipglossWidth(s string) int { return lipgloss.Width(s) }

func planFor(t *testing.T) (*plan.Plan, error) {
	t.Helper()
	s := spec.Default()
	s.Name, s.Servers, s.Agents = "test-lb1", 1, 2
	s.Network = spec.Network{Bridge: "vmbr0", FirstIP: "192.168.0.60", Prefix: 24, Gateway: "192.168.0.1"}
	s.Template = spec.Template{Storage: "local", Name: "debian-13-standard_13.1-2_amd64.tar.zst"}
	return plan.Build(s, testutil.HPInventory(t), "v1.37.2+k3s1")
}

func TestCtrlLRedraws(t *testing.T) {
	m, _ := newModel(t)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
	if cmd == nil {
		t.Fatal("Ctrl+L should return redraw commands")
	}
}
