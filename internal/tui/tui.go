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

	"github.com/scaccogatto/nastro/internal/config"
	"github.com/scaccogatto/nastro/internal/record"
	"github.com/scaccogatto/nastro/internal/records"
	"github.com/scaccogatto/nastro/internal/transcribe"
)

// chromeLines is the vertical space the list screen always reserves outside
// the list itself: status strip (2) + help footer (2). Keeping it fixed makes
// the legend always visible and the layout stable when status text appears.
// The footer stays a single (possibly clamped) line at any width rather than
// wrapping to two, so this doesn't need to vary with terminal size.
const chromeLines = 4

// footerCompactThreshold is the terminal width below which the footer
// switches to its abbreviated variant.
const footerCompactThreshold = 85

// item adapts a records.Record to list.Item. Only FilterValue is required by
// the base Item interface: everything else (name, date, duration...) is
// rendered directly from the wrapped Record by recordDelegate, so there's no
// need to also satisfy list.DefaultItem.
type item struct {
	r records.Record
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

// transcribePhase distinguishes the two visible stages of a transcribe run,
// both shown on modeTranscribing: converting the source audio (afconvert),
// before whisper-cli has produced any output, and whisper-cli actually
// running.
type transcribePhase int

const (
	transcribePreparing transcribePhase = iota
	transcribeRunning
)

// Model is the bubbletea model for the nastro TUI: the records list, and
// every screen reachable from it.
type Model struct {
	list list.Model
	cfg  config.Config
	mode screen

	// width/height are the terminal's last known size (from WindowSizeMsg),
	// used to keep every view and the footer within it.
	width, height int

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
	transcribePhase   transcribePhase
	transcribeSpinner spinner.Model
	transcribeLines   []string
	transcribeJob     *transcribe.Job
	transcribeTmpWav  string
	transcribeCancel  context.CancelFunc

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

	l := list.New(items, recordDelegate{}, 0, 0)
	l.Title = "nastro records"
	l.SetShowHelp(false)
	l.Styles = themedListStyles()

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
		m.width, m.height = msg.Width, msg.Height
		h, v := appStyle.GetFrameSize()
		m.list.SetSize(msg.Width-h, msg.Height-v-chromeLines)
		m.downloadProgress.SetWidth(min(40, max(m.contentWidth()-4, 1)))
		return m, nil

	case tea.KeyPressMsg:
		return m.updateKey(msg)

	case recStartedMsg:
		m.sess = msg.sess
		m.recSize = 0
		m.level = record.Level{}
		m.recWarning = msg.diskWarning
		m.mode = modeRecording
		return m, tea.Batch(awaitCmd(m.sess), sizeTickCmd(m.sess))

	case recStartErrMsg:
		m.recErr = msg.err
		m.mode = modeRecError
		return m, nil

	case recTickMsg:
		m.recSize = msg.size
		if m.mode != modeRecording {
			return m, nil
		}
		return m, sizeTickCmd(m.sess)

	case levelMsg:
		m.level = msg.lvl
		if m.mode != modeRecording {
			return m, nil
		}
		return m, awaitCmd(m.sess)

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
			m.statusMsg = "error opening Finder: " + msg.err.Error()
			m.statusIsErr = true
		}
		return m, nil

	case deletedMsg:
		status := "deleted " + msg.id
		isErr := msg.err != nil
		if isErr {
			status = "error deleting " + msg.id + ": " + msg.err.Error()
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
		m.transcribePhase = transcribeRunning
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
		return m.updateKeyTranscribing(msg)
	case modeTranscribeError:
		return m.updateKeyReturn(msg)
	default:
		return m.updateKeyList(msg)
	}
}

// updateKeyList handles the records list screen. While the list's own
// filter input is capturing keystrokes (FilterState == Filtering), every key
// is forwarded to it unconditionally: none of the single-letter shortcuts
// below (q included) are allowed to steal input out of the filter box.
func (m Model) updateKeyList(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.confirmDelete {
		rec, _ := m.selectedRecord()
		return m.handleDeleteConfirmKey(msg, rec)
	}
	if m.confirmOverwrite {
		return m.handleOverwriteConfirmKey(msg)
	}
	if m.list.FilterState() == list.Filtering {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "r":
		m.statusMsg = ""
		m.nameInput = textinput.New()
		m.nameInput.Placeholder = "recording name, enter for timestamp"
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

// updateKeyRecording handles the recording screen. q, s, and ctrl+c are all
// stop-and-save (the safe, non-destructive action); only x asks to discard.
func (m Model) updateKeyRecording(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.confirmingQuit {
		if isConfirmYes(msg.String()) {
			m.confirmingQuit = false
			m.killRequested = true
			return m, killCmd(m.sess)
		}
		m.confirmingQuit = false
		return m, nil
	}

	switch msg.String() {
	case "s", "q", "ctrl+c":
		m.stopRequested = true
		return m, tea.Batch(signalCmd(m.sess, os.Interrupt), killTimeoutCmd())
	case "x":
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
	if !isConfirmYes(msg.String()) {
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

// updateKeyTranscribing handles the transcribing screen: esc and ctrl+c both
// cancel the in-flight run (whichever subprocess is currently running --
// afconvert or whisper-cli) rather than being swallowed or quitting the app.
// The actual state transition happens once the canceled run reports back
// (see handleTranscribeStarted/handleTranscribeDone), not here.
func (m Model) updateKeyTranscribing(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		if m.transcribeCancel != nil {
			m.transcribeCancel()
		}
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

// contentWidth is the terminal width available to view content, after
// appStyle's margin. Falls back to a sensible default before the first
// WindowSizeMsg (or in tests that never send one).
func (m Model) contentWidth() int {
	if m.width <= 0 {
		return 76
	}
	h, _ := appStyle.GetFrameSize()
	if w := m.width - h; w >= 1 {
		return w
	}
	return 1
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

// startTranscribingScreen enters modeTranscribing immediately (spinner
// showing "preparing audio…") and kicks off the slower conversion +
// whisper-cli run in the background, so there's no dead gap before any
// feedback appears -- afconvert alone can take several seconds on a long
// recording.
func (m Model) startTranscribingScreen(rec records.Record) (Model, tea.Cmd) {
	ctx, cancel := context.WithCancel(context.Background())
	m.mode = modeTranscribing
	m.transcribePhase = transcribePreparing
	m.transcribeStart = time.Now()
	m.transcribeLines = nil
	m.transcribeJob = nil
	m.transcribeCancel = cancel
	return m, tea.Batch(m.transcribeSpinner.Tick, startTranscribeRunCmd(ctx, m.cfg, rec))
}

// cancelTranscribeCtx tears down the in-flight transcribe run's context.
// Idempotent: safe to call once the run has already finished on its own.
func (m Model) cancelTranscribeCtx() Model {
	if m.transcribeCancel != nil {
		m.transcribeCancel()
		m.transcribeCancel = nil
	}
	return m
}

func (m Model) handleDeleteConfirmKey(msg tea.KeyPressMsg, rec records.Record) (Model, tea.Cmd) {
	m.confirmDelete = false
	if !isConfirmYes(msg.String()) {
		return m, nil
	}
	m.mode = modeList
	return m, deleteCmd(rec, m.cfg.OutputDir)
}

func (m Model) handleOverwriteConfirmKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	m.confirmOverwrite = false
	if !isConfirmYes(msg.String()) {
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
		return m.startTranscribingScreen(m.transcribeTarget)
	}
}

func (m Model) handleDownloadDone(msg downloadDoneMsg) (Model, tea.Cmd) {
	m.downloadJob = nil
	switch {
	case msg.err == nil:
		return m.startTranscribingScreen(m.transcribeTarget)
	case errors.Is(msg.err, context.Canceled):
		m.mode = m.transcribeReturn
		m.statusMsg = "download canceled"
		m.statusIsErr = false
		return m, nil
	default:
		m.transcribeErr = msg.err
		m.mode = modeTranscribeError
		return m, nil
	}
}

func (m Model) handleTranscribeStarted(msg transcribeStartedMsg) (Model, tea.Cmd) {
	if errors.Is(msg.err, context.Canceled) {
		m = m.cancelTranscribeCtx()
		m.mode = m.transcribeReturn
		m.statusMsg = "transcription canceled"
		m.statusIsErr = false
		return m, nil
	}
	if msg.err != nil {
		m = m.cancelTranscribeCtx()
		m.transcribeErr = msg.err
		m.mode = modeTranscribeError
		return m, nil
	}
	m.transcribeJob = msg.job
	m.transcribeTmpWav = msg.tmpWavPath
	return m, awaitTranscribeCmd(m.transcribeJob)
}

func (m Model) handleTranscribeDone(msg transcribeDoneMsg) (Model, tea.Cmd) {
	m = m.cancelTranscribeCtx()
	if m.transcribeTmpWav != "" {
		os.Remove(m.transcribeTmpWav)
		m.transcribeTmpWav = ""
	}
	m.transcribeJob = nil

	if errors.Is(msg.err, context.Canceled) {
		m.mode = m.transcribeReturn
		m.statusMsg = "transcription canceled"
		m.statusIsErr = false
		return m, nil
	}
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
		status := truncatedPathLine("saved ", sess.RecordDir, m.contentWidth())
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
	width := m.contentWidth()
	var body string
	switch m.mode {
	case modeRecording:
		body = m.recordingView()
	case modeRecError:
		body = bodyStyle(width).Render(fmt.Sprintf("error\n\n%v", m.recErr))
	case modeNameForm:
		body = m.nameFormView()
	case modeDetail:
		body = m.detailView()
	case modeTranscribeMissingWhisper:
		body = bodyStyle(width).Render("whisper-cli not found.\n\nInstall it with: brew install whisper-cpp")
	case modeTranscribeDownloadConfirm:
		body = bodyStyle(width).Render(formatDownloadPrompt(m.cfg.WhisperModel))
	case modeDownloading:
		body = m.downloadingView()
	case modeTranscribing:
		body = m.transcribingView()
	case modeTranscribeError:
		body = bodyStyle(width).Render(fmt.Sprintf("transcription error\n\n%v", m.transcribeErr))
	default:
		body = m.listView()
	}

	if footer := m.footer(); footer != "" {
		body += "\n\n" + footerStyle.Render(footer)
	}

	v := tea.NewView(appStyle.Render(body))
	v.AltScreen = true
	return v
}

func (m Model) listView() string {
	body := m.list.View()
	if len(m.list.Items()) == 0 {
		body = m.emptyStateBody()
	}

	// Status strip: always exactly one line (possibly empty) + one blank,
	// so the layout never jumps and the space reserved by chromeLines is
	// used deterministically. Confirms take precedence over status text.
	strip := ""
	width := m.contentWidth()
	switch {
	case m.confirmDelete:
		if rec, ok := m.selectedRecord(); ok {
			strip = errStyle.Render(clampWidth(confirmDeletePrompt(rec), width))
		}
	case m.confirmOverwrite:
		strip = errStyle.Render(clampWidth(confirmOverwritePrompt(m.transcribeTarget), width))
	case m.statusMsg != "":
		strip = m.renderStatus()
	}
	return strip + "\n\n" + body
}

// emptyStateBody replaces bubbles' generic "No items." with an invitation to
// action, shown when there are no recordings at all yet.
func (m Model) emptyStateBody() string {
	return accentStyle.Render("no recordings yet") + "\npress r to record your first call"
}

func (m Model) renderStatus() string {
	msg := m.statusMsg
	// Status messages built by truncatedPathLine already carry an OSC 8
	// hyperlink and are already clamped to width; clamping them again with
	// an ANSI-code-agnostic pass could split the escape sequence.
	if !strings.Contains(msg, "\x1b]8;;") {
		msg = clampWidth(msg, m.contentWidth())
	}
	if m.statusIsErr {
		return errStyle.Render(msg)
	}
	return accentStyle.Render(msg)
}

func (m Model) nameFormView() string {
	return "recording name, enter for timestamp\n\n" + m.nameInput.View()
}

func (m Model) detailView() string {
	r := m.detailRec
	width := m.contentWidth()
	duration := "-"
	if r.HasDuration {
		duration = records.FormatDuration(r.DurationSeconds)
	}
	transcript := "-"
	if r.HasTranscript {
		transcript = "✓"
	}

	lines := []string{
		recordDisplayName(r),
		r.Date.Format("2006-01-02 15:04"),
		"duration: " + duration,
		"size: " + records.FormatSize(r.SizeBytes),
		truncatedPathLine("path: ", m.detailPath, width),
		"transcript: " + transcript,
	}
	if m.statusMsg != "" {
		lines = append(lines, "", m.renderStatus())
	}

	switch {
	case m.confirmDelete:
		lines = append(lines, "", errStyle.Render(clampWidth(confirmDeletePrompt(m.detailRec), width)))
	case m.confirmOverwrite:
		lines = append(lines, "", errStyle.Render(clampWidth(confirmOverwritePrompt(m.transcribeTarget), width)))
	case len(m.detailPreview) > 0:
		lines = append(lines, "", "--- transcript (preview) ---")
		lines = append(lines, wrapLines(m.detailPreview, width)...)
	}

	return strings.Join(lines, "\n")
}

func (m Model) recordingView() string {
	width := m.contentWidth()
	elapsed := time.Since(m.sess.Start)
	lines := []string{
		recDotStyle.Render("●") + " " + recStatusLine(elapsed),
	}
	if l := m.levelLine(); l != "" {
		lines = append(lines, l)
	}
	lines = append(lines, "", truncatedPathLine("", m.sess.RecordDir, width), records.FormatSize(m.recSize))
	if m.recWarning != "" {
		lines = append(lines, errStyle.Render(clampWidth("warning: "+m.recWarning, width)))
	}
	if m.confirmingQuit {
		lines = append(lines, "", errStyle.Render(confirmDiscardPrompt()))
	}
	return strings.Join(lines, "\n")
}

// levelLine renders the recording screen's VU meter, omitting whichever
// source hasn't reported a level yet (an older nastro-tap that never emits
// levels at all just means this stays empty forever: silent degradation).
func (m Model) levelLine() string {
	var parts []string
	if m.level.HasSystem {
		parts = append(parts, "system "+renderLevelBar(m.level.System))
	}
	if m.level.HasMic {
		parts = append(parts, "mic "+renderLevelBar(m.level.Mic))
	}
	return strings.Join(parts, "  ")
}

func (m Model) downloadingView() string {
	return fmt.Sprintf("downloading model %s...\n\n%s", m.cfg.WhisperModel, m.downloadProgress.ViewAs(m.downloadPct))
}

func (m Model) transcribingView() string {
	elapsed := time.Since(m.transcribeStart)
	label := "transcribing..."
	if m.transcribePhase == transcribePreparing {
		label = "preparing audio..."
	}
	width := m.contentWidth()
	lines := []string{
		m.transcribeSpinner.View() + " " + label + " " + records.FormatDuration(elapsed.Seconds()),
		"",
	}
	lines = append(lines, wrapLines(m.transcribeLines, width)...)
	return strings.Join(lines, "\n")
}

// wrapLines word-wraps each of lines to width, for body text (like a
// transcript preview or whisper-cli's own output) that isn't already
// guaranteed to fit.
func wrapLines(lines []string, width int) []string {
	out := make([]string, len(lines))
	style := bodyStyle(width)
	for i, l := range lines {
		out[i] = style.Render(l)
	}
	return out
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

// footer computes the help footer for the current mode, terminal width, and
// (for the list) filter/empty-list state. Always a single clamped line.
func (m Model) footer() string {
	width := m.contentWidth()
	if m.mode == modeList {
		switch {
		case m.list.FilterState() == list.Filtering:
			return clampWidth("enter apply · esc cancel", width)
		case len(m.list.Items()) == 0:
			return clampWidth("r rec · q quit", width)
		}
	}
	return clampWidth(footerFor(m.mode, width < footerCompactThreshold), width)
}

// footerFor renders the static help footer for mode, compact when the
// terminal is narrow.
func footerFor(mode screen, compact bool) string {
	switch mode {
	case modeList:
		if compact {
			return "r rec · ⏎ detail · t txs · o fndr · d del · / flt · q/ctrl+c quit"
		}
		return "r rec · enter detail · t transcribe · o finder · d delete · / filter · q/ctrl+c quit"
	case modeDetail:
		if compact {
			return "t txs · o fndr · d del · esc list"
		}
		return "t transcribe · o finder · d delete · esc list"
	case modeRecording:
		return "s stop & save · x discard · ctrl+c stop & save"
	case modeNameForm:
		return "enter confirm · esc cancel"
	case modeDownloading:
		return "esc cancel download"
	case modeTranscribing:
		return "esc cancel · ctrl+c cancel"
	case modeTranscribeDownloadConfirm:
		return "y download · any other key cancel"
	case modeRecError, modeTranscribeMissingWhisper, modeTranscribeError:
		return "press any key to continue"
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
