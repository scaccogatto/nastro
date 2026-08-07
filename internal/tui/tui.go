// Package tui implements the bubbletea v2 records list shown by bare
// `nastro` and `nastro records` (without --plain), plus the recording
// screen reachable from it.
package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/scaccogatto/nastro/internal/config"
	"github.com/scaccogatto/nastro/internal/record"
	"github.com/scaccogatto/nastro/internal/records"
)

var appStyle = lipgloss.NewStyle().Margin(1, 2)
var recDotStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)

// item adapts a records.Record to list.DefaultItem.
type item struct {
	r records.Record
}

func (i item) Title() string {
	if i.r.Slug != "" {
		return i.r.Slug
	}
	return i.r.ID
}

func (i item) Description() string {
	duration := "-"
	if i.r.HasDuration {
		duration = records.FormatDuration(i.r.DurationSeconds)
	}
	transcript := "-"
	if i.r.HasTranscript {
		transcript = "✓"
	}
	return fmt.Sprintf("%s · %s · %s · transcript %s",
		i.r.Date.Format("2006-01-02 15:04"), duration, records.FormatSize(i.r.SizeBytes), transcript)
}

func (i item) FilterValue() string { return i.r.ID + " " + i.r.Slug }

// screen identifies which of the app's screens is active.
type screen int

const (
	modeList screen = iota
	modeRecording
	modeRecError
)

// Model is the bubbletea model for the nastro TUI: the records list, and
// the recording screen reachable from it via 'r'.
type Model struct {
	list list.Model
	cfg  config.Config
	mode screen

	// recording screen state
	sess           *record.Session
	recSize        int64
	confirmingQuit bool
	stopRequested  bool
	killRequested  bool
	recErr         error

	statusMsg string // feedback shown above the list after returning from a recording
}

// New builds a Model listing recs, configured to start recordings under cfg.
func New(cfg config.Config, recs []records.Record) Model {
	items := make([]list.Item, len(recs))
	for i, r := range recs {
		items[i] = item{r: r}
	}

	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "nastro records"
	l.SetShowHelp(true)

	return Model{list: l, cfg: cfg}
}

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		h, v := appStyle.GetFrameSize()
		m.list.SetSize(msg.Width-h, msg.Height-v)
		return m, nil

	case tea.KeyPressMsg:
		return m.updateKey(msg)

	case recStartedMsg:
		m.sess = msg.sess
		m.recSize = 0
		m.mode = modeRecording
		return m, awaitOrTickCmd(m.sess)

	case recStartErrMsg:
		m.recErr = msg.err
		m.mode = modeRecError
		return m, nil

	case recTickMsg:
		m.recSize = msg.size
		return m, awaitOrTickCmd(m.sess)

	case killTimeoutMsg:
		if m.mode == modeRecording && m.stopRequested && !m.killRequested {
			return m, killCmd(m.sess)
		}
		return m, nil

	case tapExitedMsg:
		return m.handleTapExited(msg)

	case recordsReloadedMsg:
		m.statusMsg = msg.status
		if msg.err == nil {
			items := make([]list.Item, len(msg.recs))
			for i, r := range msg.recs {
				items[i] = item{r: r}
			}
			m.list.SetItems(items)
		}
		return m, nil
	}

	if m.mode != modeList {
		return m, nil
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m Model) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeRecording:
		return m.updateKeyRecording(msg)
	case modeRecError:
		m.recErr = nil
		m.mode = modeList
		m.statusMsg = ""
		return m, rescanCmd(m.cfg.OutputDir, "")
	default:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "r":
			m.statusMsg = ""
			return m, startRecordingCmd(m.cfg)
		case "enter":
			// no detail view yet, v0.2 scope
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m Model) updateKeyRecording(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.confirmingQuit {
		switch msg.String() {
		case "y":
			m.confirmingQuit = false
			m.killRequested = true
			return m, killCmd(m.sess)
		default:
			m.confirmingQuit = false
			return m, nil
		}
	}

	switch msg.String() {
	case "s", "ctrl+c":
		m.stopRequested = true
		return m, tea.Batch(signalCmd(m.sess, os.Interrupt), killTimeoutCmd())
	case "q":
		m.confirmingQuit = true
		return m, nil
	}
	return m, nil
}

func (m Model) handleTapExited(msg tapExitedMsg) (tea.Model, tea.Cmd) {
	sess := m.sess

	switch {
	case m.killRequested:
		sess.Release()
		_ = sess.Discard()
		m.sess = nil
		m.mode = modeList
		m.stopRequested, m.killRequested = false, false
		return m, rescanCmd(m.cfg.OutputDir, "discarded")

	case m.stopRequested:
		sess.Release()
		status := "saved " + sess.RecordDir
		if record.ShouldDiscard(time.Since(sess.Start)) {
			_ = sess.Discard()
			status = "discarded (shorter than 2s)"
		}
		m.sess = nil
		m.mode = modeList
		m.stopRequested, m.killRequested = false, false
		return m, rescanCmd(m.cfg.OutputDir, status)

	default:
		// Unrequested exit: crash, or (most commonly) a TCC permission
		// failure right at startup.
		m.recErr = record.DescribeTapExit(msg.err, sess.Stderr.String())
		sess.Release()
		m.sess = nil
		m.mode = modeRecError
		return m, nil
	}
}

func (m Model) View() tea.View {
	var body string
	switch m.mode {
	case modeRecording:
		body = m.recordingView()
	case modeRecError:
		body = fmt.Sprintf("errore\n\n%v\n\npremi un tasto per tornare alla lista", m.recErr)
	default:
		listBody := m.list.View()
		if m.statusMsg != "" {
			listBody = m.statusMsg + "\n\n" + listBody
		}
		body = listBody
	}

	v := tea.NewView(appStyle.Render(body))
	v.AltScreen = true
	return v
}

func (m Model) recordingView() string {
	elapsed := time.Since(m.sess.Start)
	lines := []string{
		recDotStyle.Render("●") + " " + recStatusLine(elapsed),
		"",
		m.sess.RecordDir,
		records.FormatSize(m.recSize),
		"",
	}
	if m.confirmingQuit {
		lines = append(lines, "scartare la registrazione senza salvare? [y/n]")
	} else {
		lines = append(lines, "[s]top  [q]uit senza salvare")
	}
	return strings.Join(lines, "\n")
}

// recStatusLine renders the elapsed-time half of the "● REC mm:ss" status
// line (the dot and its color are applied separately, at render time).
func recStatusLine(elapsed time.Duration) string {
	return "REC " + records.FormatDuration(elapsed.Seconds())
}

// Run starts the nastro TUI program.
func Run(cfg config.Config, recs []records.Record) error {
	_, err := tea.NewProgram(New(cfg, recs)).Run()
	return err
}
