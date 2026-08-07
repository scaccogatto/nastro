// Package tui implements the bubbletea v2 records list shown by bare
// `nastro` and `nastro records` (without --plain), plus the recording,
// detail, and transcribe screens reachable from it. The TUI is a view over
// the same internal/record, internal/records, and internal/transcribe
// packages the CLI uses -- every screen has a headless equivalent.
package tui

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/scaccogatto/nastro/internal/config"
	"github.com/scaccogatto/nastro/internal/record"
	"github.com/scaccogatto/nastro/internal/records"
	"github.com/scaccogatto/nastro/internal/transcribe"
)

var appStyle = lipgloss.NewStyle().Margin(1, 2)
var recDotStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
var errStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
var accentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
var footerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

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
	modeNameForm
	modeDetail
	modeTranscribeMissingWhisper
	modeTranscribeDownloadConfirm
	modeDownloading
	modeTranscribing
	modeTranscribeError
)

// Model is the bubbletea model for the nastro TUI: the records list, and
// every screen reachable from it.
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
	level          record.Level
	recWarning     string

	// name form, shown before starting a recording
	nameInput textinput.Model

	// detail screen
	detailRec     records.Record
	detailPath    string
	detailPreview []string

	// delete confirmation, shared by the list and detail screens
	confirmDelete bool

	// transcribe flow, triggered from the list or detail screen
	transcribeTarget  records.Record
	transcribeReturn  screen
	confirmOverwrite  bool
	pendingModelPath  string
	transcribeErr     error
	transcribeStart   time.Time
	transcribeSpinner spinner.Model
	transcribeLines   []string
	transcribeJob     *transcribe.Job
	transcribeTmpWav  string

	downloadJob      *transcribe.DownloadJob
	downloadPct      float64
	downloadProgress progress.Model

	statusMsg   string
	statusIsErr bool
}

// New builds a Model listing recs, configured to start recordings under cfg.
func New(cfg config.Config, recs []records.Record) Model {
	items := make([]list.Item, len(recs))
	for i, r := range recs {
		items[i] = item{r: r}
	}

	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "nastro records"
	l.SetShowHelp(false)

	return Model{
		list:              l,
		cfg:               cfg,
		transcribeSpinner: spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		downloadProgress:  progress.New(),
	}
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
		m.level = record.Level{}
		m.recWarning = msg.diskWarning
		m.mode = modeRecording
		return m, awaitOrTickCmd(m.sess)

	case recStartErrMsg:
		m.recErr = msg.err
		m.mode = modeRecError
		return m, nil

	case recTickMsg:
		m.recSize = msg.size
		return m, awaitOrTickCmd(m.sess)

	case levelMsg:
		m.level = msg.lvl
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
		m.statusIsErr = msg.isErr
		if msg.err == nil {
			items := make([]list.Item, len(msg.recs))
			for i, r := range msg.recs {
				items[i] = item{r: r}
			}
			m.list.SetItems(items)
		}
		return m, nil

	case detailLoadedMsg:
		m.detailPath = msg.path
		m.detailPreview = msg.preview
		return m, nil

	case finderOpenedMsg:
		if msg.err != nil {
			m.statusMsg = "errore apertura Finder: " + msg.err.Error()
			m.statusIsErr = true
		}
		return m, nil

	case deletedMsg:
		status := "deleted " + msg.id
		isErr := msg.err != nil
		if isErr {
			status = "errore eliminazione " + msg.id + ": " + msg.err.Error()
		}
		return m, rescanCmd(m.cfg.OutputDir, status, isErr)

	case transcribePrereqMsg:
		return m.handleTranscribePrereq(msg)

	case downloadTickMsg:
		m.downloadPct = msg.pct
		return m, awaitDownloadCmd(m.downloadJob)

	case downloadDoneMsg:
		return m.handleDownloadDone(msg)

	case transcribeStartedMsg:
		return m.handleTranscribeStarted(msg)

	case transcribeLineMsg:
		m.transcribeLines = appendCapped(m.transcribeLines, msg.line, 3)
		return m, awaitTranscribeCmd(m.transcribeJob)

	case transcribeDoneMsg:
		return m.handleTranscribeDone(msg)

	case spinner.TickMsg:
		if m.mode != modeTranscribing {
			return m, nil
		}
		var cmd tea.Cmd
		m.transcribeSpinner, cmd = m.transcribeSpinner.Update(msg)
		return m, cmd
	}

	switch m.mode {
	case modeList:
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	case modeNameForm:
		var cmd tea.Cmd
		m.nameInput, cmd = m.nameInput.Update(msg)
		return m, cmd
	default:
		return m, nil
	}
}

func (m Model) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeRecording:
		return m.updateKeyRecording(msg)
	case modeRecError:
		return m.updateKeyRecError(msg)
	case modeNameForm:
		return m.updateKeyNameForm(msg)
	case modeDetail:
		return m.updateKeyDetail(msg)
	case modeTranscribeMissingWhisper:
		return m.updateKeyReturn(msg)
	case modeTranscribeDownloadConfirm:
		return m.updateKeyDownloadConfirm(msg)
	case modeDownloading:
		return m.updateKeyDownloading(msg)
	case modeTranscribing:
		return m, nil
	case modeTranscribeError:
		return m.updateKeyReturn(msg)
	default:
		return m.updateKeyList(msg)
	}
}

func (m Model) updateKeyList(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.confirmDelete {
		rec, _ := m.selectedRecord()
		return m.handleDeleteConfirmKey(msg, rec)
	}
	if m.confirmOverwrite {
		return m.handleOverwriteConfirmKey(msg)
	}

	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "r":
		m.statusMsg = ""
		m.nameInput = textinput.New()
		m.nameInput.Placeholder = "nome registrazione, invio per timestamp"
		m.mode = modeNameForm
		return m, m.nameInput.Focus()
	case "enter":
		rec, ok := m.selectedRecord()
		if !ok {
			return m, nil
		}
		m.detailRec = rec
		m.detailPath, m.detailPreview = "", nil
		m.mode = modeDetail
		return m, loadDetailCmd(rec, m.cfg.OutputDir)
	case "t":
		rec, ok := m.selectedRecord()
		if !ok {
			return m, nil
		}
		return m.startTranscribe(rec, modeList)
	case "o":
		rec, ok := m.selectedRecord()
		if !ok {
			return m, nil
		}
		return m, openFinderCmd(m.recordDir(rec))
	case "d":
		if _, ok := m.selectedRecord(); !ok {
			return m, nil
		}
		m.confirmDelete = true
		return m, nil
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m Model) updateKeyDetail(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.confirmDelete {
		return m.handleDeleteConfirmKey(msg, m.detailRec)
	}
	if m.confirmOverwrite {
		return m.handleOverwriteConfirmKey(msg)
	}

	switch msg.String() {
	case "esc":
		m.mode = modeList
		return m, nil
	case "t":
		return m.startTranscribe(m.detailRec, modeDetail)
	case "o":
		return m, openFinderCmd(m.recordDir(m.detailRec))
	case "d":
		m.confirmDelete = true
		return m, nil
	}
	return m, nil
}

func (m Model) updateKeyNameForm(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeList
		return m, nil
	case "enter":
		return m, startRecordingCmd(m.cfg, record.Options{Name: m.nameInput.Value()})
	}

	var cmd tea.Cmd
	m.nameInput, cmd = m.nameInput.Update(msg)
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

// updateKeyRecError handles the recording-error screen: any key returns to
// the list, with a fresh rescan.
func (m Model) updateKeyRecError(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.recErr = nil
	m.mode = modeList
	m.statusMsg = ""
	return m, rescanCmd(m.cfg.OutputDir, "", false)
}

// updateKeyReturn handles a message screen (missing whisper-cli, a
// transcribe error): any key returns to wherever the transcribe flow was
// triggered from.
func (m Model) updateKeyReturn(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.transcribeErr = nil
	m.mode = m.transcribeReturn
	return m, nil
}

func (m Model) updateKeyDownloadConfirm(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() != "y" {
		m.mode = m.transcribeReturn
		return m, nil
	}
	job := transcribe.StartModelDownload(transcribe.ModelDownloadURL(m.cfg.WhisperModel), m.pendingModelPath)
	m.downloadJob = job
	m.downloadPct = 0
	m.mode = modeDownloading
	return m, awaitDownloadCmd(job)
}

func (m Model) updateKeyDownloading(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "esc" && m.downloadJob != nil {
		m.downloadJob.Cancel()
	}
	return m, nil
}

// selectedRecord returns the list's currently highlighted record, if any.
func (m Model) selectedRecord() (records.Record, bool) {
	it, ok := m.list.SelectedItem().(item)
	if !ok {
		return records.Record{}, false
	}
	return it.r, true
}

func (m Model) recordDir(rec records.Record) string {
	return filepath.Join(m.cfg.OutputDir, rec.ID)
}

// startTranscribe kicks off the transcribe flow for rec, remembering
// returnTo (the list or detail screen) as where to come back to once it's
// done, one way or another. Already-transcribed records ask for an inline
// overwrite confirmation first.
func (m Model) startTranscribe(rec records.Record, returnTo screen) (Model, tea.Cmd) {
	m.transcribeTarget = rec
	m.transcribeReturn = returnTo
	m.transcribeErr = nil

	if rec.HasTranscript {
		m.confirmOverwrite = true
		return m, nil
	}
	return m, checkTranscribePrereqsCmd(m.cfg)
}

func (m Model) handleDeleteConfirmKey(msg tea.KeyPressMsg, rec records.Record) (Model, tea.Cmd) {
	m.confirmDelete = false
	if msg.String() != "y" {
		return m, nil
	}
	m.mode = modeList
	return m, deleteCmd(rec, m.cfg.OutputDir)
}

func (m Model) handleOverwriteConfirmKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	m.confirmOverwrite = false
	if msg.String() != "y" {
		return m, nil
	}
	return m, checkTranscribePrereqsCmd(m.cfg)
}

func (m Model) handleTranscribePrereq(msg transcribePrereqMsg) (Model, tea.Cmd) {
	switch {
	case msg.whisperMissing:
		m.mode = modeTranscribeMissingWhisper
		return m, nil
	case msg.modelMissing:
		m.pendingModelPath = msg.modelPath
		m.mode = modeTranscribeDownloadConfirm
		return m, nil
	default:
		return m, startTranscribeRunCmd(m.cfg, m.transcribeTarget)
	}
}

func (m Model) handleDownloadDone(msg downloadDoneMsg) (Model, tea.Cmd) {
	m.downloadJob = nil
	switch {
	case msg.err == nil:
		return m, startTranscribeRunCmd(m.cfg, m.transcribeTarget)
	case errors.Is(msg.err, context.Canceled):
		m.mode = m.transcribeReturn
		m.statusMsg = "download annullato"
		m.statusIsErr = false
		return m, nil
	default:
		m.transcribeErr = msg.err
		m.mode = modeTranscribeError
		return m, nil
	}
}

func (m Model) handleTranscribeStarted(msg transcribeStartedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.transcribeErr = msg.err
		m.mode = modeTranscribeError
		return m, nil
	}
	m.transcribeJob = msg.job
	m.transcribeTmpWav = msg.tmpWavPath
	m.transcribeStart = time.Now()
	m.transcribeLines = nil
	m.mode = modeTranscribing
	return m, tea.Batch(m.transcribeSpinner.Tick, awaitTranscribeCmd(m.transcribeJob))
}

func (m Model) handleTranscribeDone(msg transcribeDoneMsg) (Model, tea.Cmd) {
	if m.transcribeTmpWav != "" {
		os.Remove(m.transcribeTmpWav)
		m.transcribeTmpWav = ""
	}
	m.transcribeJob = nil

	if msg.err != nil {
		m.transcribeErr = msg.err
		m.mode = modeTranscribeError
		return m, nil
	}

	status := "transcribed " + m.transcribeTarget.ID
	m.mode = m.transcribeReturn
	m.statusMsg = status
	m.statusIsErr = false

	if m.mode == modeDetail {
		m.detailRec.HasTranscript = true
		return m, loadDetailCmd(m.detailRec, m.cfg.OutputDir)
	}
	return m, rescanCmd(m.cfg.OutputDir, status, false)
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
		return m, rescanCmd(m.cfg.OutputDir, "discarded", false)

	case m.stopRequested:
		sess.Release()
		status := "saved " + hyperlink(sess.RecordDir, sess.RecordDir)
		if record.ShouldDiscard(time.Since(sess.Start)) {
			_ = sess.Discard()
			status = "discarded (shorter than 2s)"
		}
		m.sess = nil
		m.mode = modeList
		m.stopRequested, m.killRequested = false, false
		return m, rescanCmd(m.cfg.OutputDir, status, false)

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
	case modeNameForm:
		body = m.nameFormView()
	case modeDetail:
		body = m.detailView()
	case modeTranscribeMissingWhisper:
		body = "whisper-cli non trovato.\n\nInstallalo con: brew install whisper-cpp"
	case modeTranscribeDownloadConfirm:
		body = fmt.Sprintf("modello %q mancante.\nScaricarlo ora da huggingface.co? [y/n]", m.cfg.WhisperModel)
	case modeDownloading:
		body = m.downloadingView()
	case modeTranscribing:
		body = m.transcribingView()
	case modeTranscribeError:
		body = fmt.Sprintf("errore trascrizione\n\n%v", m.transcribeErr)
	default:
		body = m.listView()
	}

	if footer := footerFor(m.mode); footer != "" {
		body += "\n\n" + footerStyle.Render(footer)
	}

	v := tea.NewView(appStyle.Render(body))
	v.AltScreen = true
	return v
}

func (m Model) listView() string {
	body := m.list.View()

	var prefix []string
	if m.statusMsg != "" {
		prefix = append(prefix, m.renderStatus())
	}
	if m.confirmDelete {
		prefix = append(prefix, "eliminare la registrazione selezionata? [y/n]")
	}
	if m.confirmOverwrite {
		prefix = append(prefix, "già trascritto, sovrascrivere? [y/n]")
	}
	if len(prefix) == 0 {
		return body
	}
	return strings.Join(prefix, "\n") + "\n\n" + body
}

func (m Model) renderStatus() string {
	if m.statusIsErr {
		return errStyle.Render(m.statusMsg)
	}
	return accentStyle.Render(m.statusMsg)
}

func (m Model) nameFormView() string {
	return "nome registrazione, invio per timestamp\n\n" + m.nameInput.View()
}

func (m Model) detailView() string {
	r := m.detailRec
	name := r.Slug
	if name == "" {
		name = r.ID
	}
	duration := "-"
	if r.HasDuration {
		duration = records.FormatDuration(r.DurationSeconds)
	}
	transcript := "-"
	if r.HasTranscript {
		transcript = "✓"
	}

	lines := []string{
		name,
		r.Date.Format("2006-01-02 15:04"),
		"durata: " + duration,
		"size: " + records.FormatSize(r.SizeBytes),
		"path: " + hyperlink(m.detailPath, m.detailPath),
		"transcript: " + transcript,
	}
	if m.statusMsg != "" {
		lines = append(lines, "", m.renderStatus())
	}

	switch {
	case m.confirmDelete:
		lines = append(lines, "", "eliminare questa registrazione? [y/n]")
	case m.confirmOverwrite:
		lines = append(lines, "", "già trascritto, sovrascrivere? [y/n]")
	case len(m.detailPreview) > 0:
		lines = append(lines, "", "--- transcript (preview) ---")
		lines = append(lines, m.detailPreview...)
	}

	return strings.Join(lines, "\n")
}

func (m Model) recordingView() string {
	elapsed := time.Since(m.sess.Start)
	lines := []string{
		recDotStyle.Render("●") + " " + recStatusLine(elapsed),
	}
	if l := m.levelLine(); l != "" {
		lines = append(lines, l)
	}
	lines = append(lines, "", hyperlink(m.sess.RecordDir, m.sess.RecordDir), records.FormatSize(m.recSize))
	if m.recWarning != "" {
		lines = append(lines, errStyle.Render("warning: "+m.recWarning))
	}
	if m.confirmingQuit {
		lines = append(lines, "", "scartare la registrazione senza salvare? [y/n]")
	}
	return strings.Join(lines, "\n")
}

// levelLine renders the recording screen's VU meter, omitting whichever
// source hasn't reported a level yet (an older nastro-tap that never emits
// levels at all just means this stays empty forever: silent degradation).
func (m Model) levelLine() string {
	var parts []string
	if m.level.HasSystem {
		parts = append(parts, "sistema "+renderLevelBar(m.level.System))
	}
	if m.level.HasMic {
		parts = append(parts, "mic "+renderLevelBar(m.level.Mic))
	}
	return strings.Join(parts, "  ")
}

func (m Model) downloadingView() string {
	return fmt.Sprintf("scaricamento modello %s...\n\n%s", m.cfg.WhisperModel, m.downloadProgress.ViewAs(m.downloadPct))
}

func (m Model) transcribingView() string {
	elapsed := time.Since(m.transcribeStart)
	lines := []string{
		m.transcribeSpinner.View() + " trascrizione in corso... " + records.FormatDuration(elapsed.Seconds()),
		"",
	}
	lines = append(lines, m.transcribeLines...)
	return strings.Join(lines, "\n")
}

// recStatusLine renders the elapsed-time half of the "● REC mm:ss" status
// line (the dot and its color are applied separately, at render time).
func recStatusLine(elapsed time.Duration) string {
	return "REC " + records.FormatDuration(elapsed.Seconds())
}

// renderLevelBar renders a normalized 0..1 level as an 8-cell ▮/▯ bar.
func renderLevelBar(level float64) string {
	const cells = 8
	filled := int(level*cells + 0.5)
	if filled < 0 {
		filled = 0
	}
	if filled > cells {
		filled = cells
	}
	return strings.Repeat("▮", filled) + strings.Repeat("▯", cells-filled)
}

// appendCapped appends line to lines, keeping only the last max entries --
// used for the last few lines of whisper-cli output shown while
// transcribing.
func appendCapped(lines []string, line string, max int) []string {
	lines = append(lines, line)
	if len(lines) > max {
		lines = lines[len(lines)-max:]
	}
	return lines
}

// footerFor renders the static help footer for mode.
func footerFor(mode screen) string {
	switch mode {
	case modeList:
		return "r rec · invio dettaglio · t transcribe · o finder · d elimina · / filtra · q esci"
	case modeDetail:
		return "t transcribe · o finder · d elimina · esc lista"
	case modeRecording:
		return "s stop · q annulla senza salvare"
	case modeNameForm:
		return "invio conferma · esc annulla"
	case modeDownloading:
		return "esc annulla download"
	case modeTranscribing:
		return "trascrizione in corso…"
	case modeRecError, modeTranscribeMissingWhisper, modeTranscribeDownloadConfirm, modeTranscribeError:
		return "premi un tasto per continuare"
	default:
		return ""
	}
}

// Run starts the nastro TUI program.
func Run(cfg config.Config, recs []records.Record) error {
	_, err := tea.NewProgram(New(cfg, recs)).Run()
	return err
}

// hyperlink wraps text in an OSC 8 terminal hyperlink pointing at path as a
// file:// URL, so terminals like Ghostty make it cmd+clickable (a directory
// opens in Finder). Terminals without OSC 8 support ignore the sequence and
// show the bare text.
func hyperlink(text, path string) string {
	u := url.URL{Scheme: "file", Path: path}
	return "\x1b]8;;" + u.String() + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}
