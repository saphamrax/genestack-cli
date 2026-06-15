// Package tui implements the Bubble Tea terminal UI: a three-pane dashboard
// (nodes, phases, logs) that drives a Genestack deployment over SSH. The TUI
// owns all mutable state; background command execution communicates back
// exclusively through the events channel, keeping Update single-threaded.
package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rackerlabs/genestack-cli/internal/engine"
	"github.com/rackerlabs/genestack-cli/internal/exec"
	"github.com/rackerlabs/genestack-cli/internal/inventory"
	"github.com/rackerlabs/genestack-cli/internal/model"
	"github.com/rackerlabs/genestack-cli/internal/overrides"
	"github.com/rackerlabs/genestack-cli/internal/runlog"
	"github.com/rackerlabs/genestack-cli/internal/runner"
)

// remoteInventoryPath is where the generated inventory is uploaded on the
// deployment host (shared with the CLI).
const remoteInventoryPath = engine.RemoteInventoryPath

type focusArea int

const (
	focusNodes focusArea = iota
	focusPhases
)

// --- messages emitted from background goroutines via the events channel ---

type logMsg string
type infoMsg string
type connectResultMsg struct{ err error }
type stepStatusMsg struct {
	id     string
	status engine.Status
}
type runFinishedMsg struct{ err error }
type uploadResultMsg struct{ err error }
type overridesResultMsg struct {
	err   error
	count int
}

// Model is the root Bubble Tea model.
type Model struct {
	cfgPath      string
	overridesDir string
	logDir       string
	cluster      *model.Cluster
	phases       []engine.Phase
	state        *engine.State
	ssh          exec.Executor
	runner       *runner.Runner
	events       chan tea.Msg

	vp    viewport.Model
	logs  []string
	ready bool

	focus    focusArea
	nodeIdx  int
	phaseIdx int

	width, height int

	connected bool
	running   bool
	status    string

	// add-node form overlay
	formActive bool
	inputs     []textinput.Model
	formIdx    int
}

// New builds a TUI model for the given cluster and config path. statePath is
// where run progress is persisted.
func New(cluster *model.Cluster, cfgPath, statePath string) *Model {
	executor := exec.New(exec.Config{
		Local:                cluster.Deployment.IsLocal(),
		Host:                 cluster.Deployment.Host,
		Port:                 cluster.Deployment.Port,
		User:                 cluster.Deployment.User,
		KeyPath:              cluster.Deployment.KeyPath,
		AcceptUnknownHostKey: true, // lab/bootstrap default
	})

	overridesDir := filepath.Join(filepath.Dir(cfgPath), "overrides")
	m := &Model{
		cfgPath:      cfgPath,
		overridesDir: overridesDir,
		logDir:       filepath.Join(filepath.Dir(cfgPath), "logs"),
		cluster:      cluster,
		phases:       engine.DefaultPhases(),
		state:        engine.LoadState(statePath),
		ssh:          executor,
		runner:       &runner.Runner{Exec: executor, Cluster: cluster, OverridesDir: overridesDir},
		events:       make(chan tea.Msg, 256),
		status:       "press c to connect · ? for help",
	}
	m.initForm()
	return m
}

func (m *Model) initForm() {
	labels := []string{"name (e.g. ctl01)", "ansible_host IP", "roles (controller,compute,...)"}
	m.inputs = make([]textinput.Model, len(labels))
	for i, l := range labels {
		ti := textinput.New()
		ti.Placeholder = l
		ti.CharLimit = 128
		ti.Width = 32
		m.inputs[i] = ti
	}
}

// Init satisfies tea.Model.
func (m *Model) Init() tea.Cmd {
	return m.waitForEvent()
}

// waitForEvent blocks until a background goroutine pushes a message.
func (m *Model) waitForEvent() tea.Cmd {
	return func() tea.Msg { return <-m.events }
}

func (m *Model) push(msg tea.Msg) { m.events <- msg }

// expand substitutes cluster-specific placeholders in a command.
func (m *Model) expand(cmd string) string { return m.runner.Expand(cmd) }

// --- background actions ---

func (m *Model) connectCmd() tea.Cmd {
	return func() tea.Msg {
		err := m.ssh.Connect(context.Background())
		return connectResultMsg{err: err}
	}
}

func (m *Model) uploadInventoryCmd() tea.Cmd {
	return func() tea.Msg {
		content, err := inventory.Generate(m.cluster)
		if err != nil {
			return uploadResultMsg{err: err}
		}
		if err := m.ssh.Connect(context.Background()); err != nil {
			return uploadResultMsg{err: err}
		}
		err = m.ssh.Upload(context.Background(), content, remoteInventoryPath)
		return uploadResultMsg{err: err}
	}
}

func (m *Model) uploadOverridesCmd() tea.Cmd {
	return func() tea.Msg {
		plan, err := overrides.Plan(m.cluster, m.overridesDir)
		if err != nil {
			return overridesResultMsg{err: err}
		}
		if err := m.ssh.Connect(context.Background()); err != nil {
			return overridesResultMsg{err: err}
		}
		n := 0
		for _, f := range plan {
			if err := m.ssh.Upload(context.Background(), f.Content, f.Path); err != nil {
				return overridesResultMsg{err: fmt.Errorf("%s: %w", f.Path, err)}
			}
			n++
		}
		return overridesResultMsg{count: n}
	}
}

// startRun launches a goroutine that runs the given phases sequentially,
// resuming from a status snapshot. It returns immediately; progress arrives via
// the events channel.
func (m *Model) startRun(phases []engine.Phase) tea.Cmd {
	snapshot := map[string]engine.Status{}
	for _, p := range phases {
		for _, st := range p.Steps {
			snapshot[st.ID] = m.state.Get(st.ID)
		}
	}
	return func() tea.Msg {
		go m.runSequence(phases, snapshot)
		return infoMsg("starting deployment run…")
	}
}

func (m *Model) runSequence(phases []engine.Phase, snap map[string]engine.Status) {
	ctx := context.Background()

	var rl *runlog.Logger
	if m.logDir != "" {
		if l, err := runlog.New(m.logDir); err == nil {
			rl = l
			defer rl.Close()
			m.push(logMsg("logging to " + rl.Dir()))
		}
	}

	if err := m.ssh.Connect(ctx); err != nil {
		m.push(logMsg("connect failed: " + err.Error()))
		m.push(runFinishedMsg{err: err})
		return
	}

	for _, p := range phases {
		m.push(logMsg(fmt.Sprintf("══ phase: %s ══", p.Title)))
		rl.Event("══ phase: %s ══", p.Title)
		for _, st := range p.Steps {
			switch snap[st.ID] {
			case engine.StatusDone, engine.StatusSkipped:
				continue
			}
			if st.Optional {
				// Optional steps are skipped on a bulk run; trigger them
				// individually with Enter on the step.
				m.push(stepStatusMsg{st.ID, engine.StatusSkipped})
				m.push(logMsg("  (skipped optional) " + st.Title))
				continue
			}
			if err := m.runStep(ctx, st, rl); err != nil {
				m.push(stepStatusMsg{st.ID, engine.StatusFailed})
				m.push(logMsg("  ✗ " + st.Title + ": " + err.Error()))
				m.push(runFinishedMsg{err: err})
				return
			}
			m.push(stepStatusMsg{st.ID, engine.StatusDone})
		}
	}
	m.push(runFinishedMsg{err: nil})
}

// runStep executes one step, forwarding its output as log messages (and to the
// run logger, if any). It blocks until the command exits.
func (m *Model) runStep(ctx context.Context, st engine.Step, rl *runlog.Logger) error {
	m.push(stepStatusMsg{st.ID, engine.StatusRunning})
	m.push(logMsg("▶ " + st.Title))
	rl.Event("▶ %s — %s", st.ID, st.Title)
	stepW, closeStep := rl.Step(st.ID)
	err := m.runner.RunStep(ctx, st, func(l string) {
		m.push(logMsg("    " + l))
		stepW(l)
	})
	closeStep()
	if err != nil {
		rl.Event("✗ %s: %v", st.ID, err)
	} else {
		rl.Event("✓ %s", st.ID)
	}
	return err
}

// --- update ---

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case tea.KeyMsg:
		if m.formActive {
			return m, m.updateForm(msg)
		}
		return m.handleKey(msg)

	case connectResultMsg:
		if msg.err != nil {
			m.connected = false
			m.status = "connect failed: " + msg.err.Error()
			m.appendLog("connect failed: " + msg.err.Error())
		} else {
			m.connected = true
			m.status = "connected to " + m.cluster.Deployment.Host
			m.appendLog("connected to " + m.cluster.Deployment.Host)
		}
		return m, m.waitForEvent()

	case uploadResultMsg:
		if msg.err != nil {
			m.status = "inventory upload failed: " + msg.err.Error()
			m.appendLog("inventory upload failed: " + msg.err.Error())
		} else {
			m.connected = true
			m.status = "inventory uploaded to " + remoteInventoryPath
			m.appendLog("inventory uploaded → " + remoteInventoryPath)
		}
		return m, m.waitForEvent()

	case overridesResultMsg:
		if msg.err != nil {
			m.status = "overrides upload failed: " + msg.err.Error()
			m.appendLog("overrides upload failed: " + msg.err.Error())
		} else {
			m.connected = true
			m.status = fmt.Sprintf("uploaded %d override file(s)", msg.count)
			m.appendLog(m.status)
		}
		return m, m.waitForEvent()

	case stepStatusMsg:
		m.state.Set(msg.id, msg.status)
		return m, m.waitForEvent()

	case logMsg:
		m.appendLog(string(msg))
		return m, m.waitForEvent()

	case infoMsg:
		m.status = string(msg)
		m.appendLog(string(msg))
		return m, m.waitForEvent()

	case runFinishedMsg:
		m.running = false
		if msg.err != nil {
			m.status = "run stopped: " + msg.err.Error()
		} else {
			m.status = "run finished ✓"
		}
		m.appendLog("── " + m.status + " ──")
		return m, m.waitForEvent()
	}

	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.ssh.Close()
		return m, tea.Quit

	case "tab":
		if m.focus == focusNodes {
			m.focus = focusPhases
		} else {
			m.focus = focusNodes
		}

	case "up", "k":
		if m.focus == focusNodes {
			if m.nodeIdx > 0 {
				m.nodeIdx--
			}
		} else if m.phaseIdx > 0 {
			m.phaseIdx--
		}

	case "down", "j":
		if m.focus == focusNodes {
			if m.nodeIdx < len(m.cluster.Nodes)-1 {
				m.nodeIdx++
			}
		} else if m.phaseIdx < len(m.phases)-1 {
			m.phaseIdx++
		}

	case "c":
		m.status = "connecting…"
		return m, m.connectCmd()

	case "g":
		m.status = "generating & uploading inventory…"
		return m, m.uploadInventoryCmd()

	case "o":
		m.status = "generating & uploading overrides…"
		return m, m.uploadOverridesCmd()

	case "a":
		m.openForm()

	case "d":
		// delete focused node
		if m.focus == focusNodes && len(m.cluster.Nodes) > 0 {
			name := m.cluster.Nodes[m.nodeIdx].Name
			m.cluster.RemoveNode(name)
			if m.nodeIdx >= len(m.cluster.Nodes) && m.nodeIdx > 0 {
				m.nodeIdx--
			}
			_ = m.cluster.Save(m.cfgPath)
			m.appendLog("removed node " + name)
		}

	case "enter":
		// run the selected phase
		if m.focus == focusPhases && !m.running && len(m.phases) > 0 {
			m.running = true
			p := m.phases[m.phaseIdx]
			m.status = "running phase: " + p.Title
			return m, m.startRun([]engine.Phase{p})
		}

	case "R":
		// run all phases from the selected one onward
		if !m.running && len(m.phases) > 0 {
			m.running = true
			rest := m.phases[m.phaseIdx:]
			m.status = "running all phases from: " + rest[0].Title
			return m, m.startRun(rest)
		}

	case "pgup":
		m.vp.HalfViewUp()
	case "pgdown":
		m.vp.HalfViewDown()
	}

	return m, nil
}

// --- add-node form ---

func (m *Model) openForm() {
	m.formActive = true
	m.formIdx = 0
	for i := range m.inputs {
		m.inputs[i].Reset()
		m.inputs[i].Blur()
	}
	m.inputs[0].Focus()
}

func (m *Model) updateForm(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.formActive = false
		return nil
	case "tab", "down":
		m.inputs[m.formIdx].Blur()
		m.formIdx = (m.formIdx + 1) % len(m.inputs)
		m.inputs[m.formIdx].Focus()
		return nil
	case "shift+tab", "up":
		m.inputs[m.formIdx].Blur()
		m.formIdx = (m.formIdx - 1 + len(m.inputs)) % len(m.inputs)
		m.inputs[m.formIdx].Focus()
		return nil
	case "enter":
		m.submitForm()
		return nil
	}
	var cmd tea.Cmd
	m.inputs[m.formIdx], cmd = m.inputs[m.formIdx].Update(msg)
	return cmd
}

func (m *Model) submitForm() {
	name := strings.TrimSpace(m.inputs[0].Value())
	ip := strings.TrimSpace(m.inputs[1].Value())
	rolesRaw := strings.TrimSpace(m.inputs[2].Value())

	var roles []model.Role
	for _, r := range strings.Split(rolesRaw, ",") {
		r = strings.TrimSpace(r)
		if r != "" {
			roles = append(roles, model.Role(r))
		}
	}
	n := model.Node{Name: name, AnsibleHost: ip, Roles: roles}
	if err := m.cluster.AddNode(n); err != nil {
		m.status = "add node failed: " + err.Error()
		m.appendLog("add node failed: " + err.Error())
		return
	}
	_ = m.cluster.Save(m.cfgPath)
	m.formActive = false
	m.status = "added node " + name
	m.appendLog("added node " + name + " (" + ip + ")")
}

// --- layout & view ---

func (m *Model) appendLog(s string) {
	for _, line := range strings.Split(s, "\n") {
		m.logs = append(m.logs, line)
	}
	if len(m.logs) > 5000 {
		m.logs = m.logs[len(m.logs)-5000:]
	}
	if m.ready {
		m.vp.SetContent(strings.Join(m.logs, "\n"))
		m.vp.GotoBottom()
	}
}

func (m *Model) layout() {
	if m.width == 0 || m.height == 0 {
		return
	}
	logHeight := m.height/3 - 1
	if logHeight < 4 {
		logHeight = 4
	}
	logWidth := m.width - 4
	if !m.ready {
		m.vp = viewport.New(logWidth, logHeight)
		m.ready = true
	} else {
		m.vp.Width = logWidth
		m.vp.Height = logHeight
	}
	m.vp.SetContent(strings.Join(m.logs, "\n"))
	m.vp.GotoBottom()
}

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	paneStyle     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	focusedPane   = paneStyle.BorderForeground(lipgloss.Color("63"))
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	okStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	runStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	failStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
)

func statusStyle(s engine.Status) lipgloss.Style {
	switch s {
	case engine.StatusDone, engine.StatusSkipped:
		return okStyle
	case engine.StatusRunning:
		return runStyle
	case engine.StatusFailed:
		return failStyle
	default:
		return dimStyle
	}
}

func (m *Model) View() string {
	if !m.ready {
		return "initialising…"
	}
	if m.formActive {
		return m.formView()
	}

	topH := m.height - m.vp.Height - 5
	if topH < 6 {
		topH = 6
	}
	leftW := m.width/3 - 2
	rightW := m.width - leftW - 6

	left := m.nodesView(leftW, topH)
	right := m.phasesView(rightW, topH)

	ls := paneStyle
	rs := paneStyle
	if m.focus == focusNodes {
		ls = focusedPane
	} else {
		rs = focusedPane
	}
	top := lipgloss.JoinHorizontal(lipgloss.Top,
		ls.Width(leftW).Height(topH).Render(left),
		rs.Width(rightW).Height(topH).Render(right),
	)

	logs := paneStyle.Width(m.width - 4).Render(
		titleStyle.Render("Logs") + "\n" + m.vp.View())

	return top + "\n" + logs + "\n" + m.footer()
}

func (m *Model) nodesView(w, h int) string {
	var b strings.Builder
	conn := failStyle.Render("offline")
	if m.connected {
		conn = okStyle.Render("online")
	}
	target := m.cluster.Deployment.Host
	if m.cluster.Deployment.IsLocal() {
		target = "local (this host)"
	}
	b.WriteString(titleStyle.Render("Nodes") + "  " + dimStyle.Render(target+" ") + conn + "\n")
	if len(m.cluster.Nodes) == 0 {
		b.WriteString(dimStyle.Render("(none — press 'a' to add)"))
		return b.String()
	}
	for i, n := range m.cluster.Nodes {
		roles := make([]string, 0, len(n.Roles))
		for _, r := range n.Roles {
			roles = append(roles, string(r)[:1])
		}
		line := fmt.Sprintf("%-12s %-15s [%s]", trunc(n.Name, 12), n.AnsibleHost, strings.Join(roles, ""))
		if m.focus == focusNodes && i == m.nodeIdx {
			b.WriteString(selectedStyle.Render("› "+line) + "\n")
		} else {
			b.WriteString("  " + line + "\n")
		}
	}
	return b.String()
}

func (m *Model) phasesView(w, h int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Phases") + "\n")
	for i, p := range m.phases {
		st, done, total := m.state.PhaseStatus(p)
		ss := statusStyle(st)
		head := fmt.Sprintf("%s %-26s %d/%d", ss.Render(st.Icon()), trunc(p.Title, 26), done, total)
		if i == m.phaseIdx {
			b.WriteString(selectedStyle.Render("› "+head) + "\n")
			// expand selected phase: list its steps
			for _, step := range p.Steps {
				sst := m.state.Get(step.ID)
				opt := ""
				if step.Optional {
					opt = dimStyle.Render(" (opt)")
				}
				b.WriteString("    " + statusStyle(sst).Render(sst.Icon()) + " " + trunc(step.Title, w-10) + opt + "\n")
			}
		} else {
			b.WriteString("  " + head + "\n")
		}
	}
	return b.String()
}

func (m *Model) formView() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Add node") + "\n\n")
	for i, in := range m.inputs {
		cursor := "  "
		if i == m.formIdx {
			cursor = selectedStyle.Render("› ")
		}
		b.WriteString(cursor + in.View() + "\n")
	}
	b.WriteString("\n" + dimStyle.Render("tab/↑↓ move · enter save · esc cancel"))
	return paneStyle.Width(m.width - 4).Render(b.String())
}

func (m *Model) footer() string {
	keys := "c connect · g inventory · o overrides · a add · d del · enter run phase · R run all · tab focus · q quit"
	run := ""
	if m.running {
		run = runStyle.Render(" [running] ")
	}
	return dimStyle.Render(keys) + "\n" + titleStyle.Render(" "+m.status+" ") + run
}

func trunc(s string, n int) string {
	if n < 1 {
		n = 1
	}
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
