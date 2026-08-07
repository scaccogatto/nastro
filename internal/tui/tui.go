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
	"charm.land/bubbles/v2/paginator"
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
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
	modeHelp
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

	// homeDir is the user's home directory, resolved once at New() time
	// (empty if it couldn't be): used to abbreviate displayed paths to "~"
	// and to locate ~/.Trash and ~/.config/nastro/config.toml.
	homeDir string

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
	nameInput    textinput.Model
	nameFormMode captureMode

	// detail screen
	detailRec     records.Record
	detailPath    string
	detailPreview []string

	// delete confirmation, shared by the list and detail screens
	confirmDelete bool

	// transcribe flow, triggered from the list or detail screen
	transcribeTarget   records.Record
	transcribeReturn   screen
	confirmOverwrite   bool
	pendingModelPath   string
	transcribeErr      error
	transcribeStart    time.Time
	transcribePhase    transcribePhase
	transcribeSpinner  spinner.Model
	transcribeLines    []string
	transcribeJob      *transcribe.Job
	transcribeTmpWav   string
	transcribeCancel   context.CancelFunc
	transcribeProgress progress.Model
	transcribePct      float64
	transcribeHasPct   bool

	downloadJob      *transcribe.DownloadJob
	downloadPct      float64
	downloadProgress progress.Model

	// help overlay, entered from the list
	helpViewport viewport.Model

	// savedStatusDir/-Duration hold the raw (untruncated) data behind the
	// "✓ saved <duration> · <path>" status shown right after a stop-and-save,
	// so the clickable path gets re-truncated to the terminal's *current*
	// width on every render (see renderStatus) instead of being baked in,
	// stale, at save time. Empty dir means no saved-status is showing.
	savedStatusDir      string
	savedStatusDuration float64

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
	l.Paginator.Type = paginator.Arabic

	home, _ := os.UserHomeDir()

	return Model{
		list:               l,
		cfg:                cfg,
		homeDir:            home,
		transcribeSpinner:  spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		transcribeProgress: newProgress(),
		downloadProgress:   newProgress(),
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
		m.transcribeProgress.SetWidth(min(40, max(m.contentWidth()-4, 1)))
		m.helpViewport.SetWidth(m.contentWidth())
		m.helpViewport.SetHeight(m.contentHeight())
		if m.mode == modeHelp {
			m.helpViewport.SetContent(helpContent(m))
		}
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
		// An empty status is a silent background rescan (H4): leave
		// whatever's currently showing -- notably a just-set savedStatusDir
		// (see handleTapExited) -- alone rather than clobbering it with the
		// rescan it happens to be piggybacking on.
		if msg.status != "" {
			m.statusMsg = msg.status
			m.statusIsErr = msg.isErr
			m.savedStatusDir = ""
		}
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
		status, isErr := deleteStatus(msg)
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
		if pct, ok := transcribe.ParseWhisperProgress(msg.line); ok {
			m.transcribeHasPct = true
			m.transcribePct = float64(pct) / 100
		} else {
			m.transcribeLines = appendCapped(m.transcribeLines, msg.line, 3)
		}
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
	case modeHelp:
		return m.updateKeyHelp(msg)
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
		m.nameFormMode = captureMixed
		m.mode = modeNameForm
		return m, m.nameInput.Focus()
	case "?":
		return m.enterHelp(), nil
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
		m.statusMsg = ""
		m.savedStatusDir = ""
		return m, rescanCmd(m.cfg.OutputDir, "", false)
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
	case "esc", "ctrl+c":
		m.mode = modeList
		return m, nil
	case "tab":
		m.nameFormMode = nextCaptureMode(m.nameFormMode)
		return m, nil
	case "enter":
		micOnly, systemOnly := captureModeOptions(m.nameFormMode)
		return m, startRecordingCmd(m.cfg, record.Options{Name: m.nameInput.Value(), MicOnly: micOnly, SystemOnly: systemOnly})
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

// updateKeyHelp handles the help overlay: ↑/↓/pgup/pgdn scroll its viewport,
// any other key closes it, back to the list -- the only screen it's
// reachable from.
func (m Model) updateKeyHelp(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "down", "pgup", "pgdown":
		var cmd tea.Cmd
		m.helpViewport, cmd = m.helpViewport.Update(msg)
		return m, cmd
	}
	m.mode = modeList
	return m, nil
}

// enterHelp switches to the help overlay, (re)sizing and filling its
// viewport from the model's current width/height -- already known by the
// time "?" can be pressed, since WindowSizeMsg arrives before any key does.
func (m Model) enterHelp() Model {
	m.mode = modeHelp
	m.helpViewport.SetWidth(m.contentWidth())
	m.helpViewport.SetHeight(m.contentHeight())
	m.helpViewport.SetContent(helpContent(m))
	return m
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
	switch msg.String() {
	case "esc", "ctrl+c":
		if m.downloadJob != nil {
			m.downloadJob.Cancel()
		}
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

// contentHeight is the terminal height available to view content, after
// appStyle's margin and the blank line + footer line View() always appends
// below the body. Falls back to a sensible default before the first
// WindowSizeMsg (or in tests that never send one).
func (m Model) contentHeight() int {
	if m.height <= 0 {
		return 20
	}
	_, v := appStyle.GetFrameSize()
	if h := m.height - v - 2; h >= 1 {
		return h
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
	m.transcribePct = 0
	m.transcribeHasPct = false
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
	return m, deleteCmd(rec, m.cfg.OutputDir, m.trashDir())
}

// trashDir is the user's ~/.Trash, or "" if the home directory couldn't be
// resolved -- deleteCmd then falls back to permanent removal.
func (m Model) trashDir() string {
	if m.homeDir == "" {
		return ""
	}
	return filepath.Join(m.homeDir, ".Trash")
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
		if m.mode == modeList {
			return m, rescanCmd(m.cfg.OutputDir, m.statusMsg, false)
		}
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
		if m.mode == modeList {
			return m, rescanCmd(m.cfg.OutputDir, m.statusMsg, false)
		}
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
		dur := time.Since(sess.Start).Seconds()
		if b, err := os.ReadFile(filepath.Join(sess.RecordDir, "metadata.json")); err == nil {
			if d, ok := records.ParseMetadata(b); ok {
				dur = d
			}
		}
		m.sess = nil
		m.mode = modeList
		m.stopRequested, m.killRequested = false, false
		if record.ShouldDiscard(time.Since(sess.Start)) {
			_ = sess.Discard()
			return m, rescanCmd(m.cfg.OutputDir, "discarded (shorter than 2s)", false)
		}
		// Raw (untruncated) data, not a pre-rendered string: renderStatus
		// formats it with the terminal's *current* width on every render,
		// so it doesn't go stale if the terminal is resized afterward.
		m.savedStatusDir, m.savedStatusDuration = sess.RecordDir, dur
		return m, rescanCmd(m.cfg.OutputDir, "", false)

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
		body = bodyStyle(width).Render("transcription error\n\n" + transcribe.FriendlyTranscribeError(m.transcribeErr, strings.Join(m.transcribeLines, "\n")))
	case modeHelp:
		body = m.helpViewport.View()
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
	case m.statusMsg != "" || m.savedStatusDir != "":
		strip = m.renderStatus()
	}
	return strip + "\n\n" + body
}

// emptyStateBody replaces bubbles' generic "No items." with an invitation to
// action, shown when there are no recordings at all yet -- keeping the
// list's own title above it rather than losing it along with the rest of
// the (now-empty) list body.
func (m Model) emptyStateBody() string {
	title := m.list.Styles.Title.Render(m.list.Title)
	return title + "\n\n" + accentStyle.Render("no recordings yet") + "\npress r to record your first call"
}

// renderStatus renders the list's status strip. Re-clamped to the
// terminal's *current* width on every call (rather than once, at the time
// the status was set) so a resize afterward doesn't leave a stale
// truncation behind -- see savedStatusDir's doc comment for the case that
// actually bit us (a hyperlinked path baked in at save time).
func (m Model) renderStatus() string {
	if m.savedStatusDir != "" {
		return accentStyle.Render(formatSavedStatus(m.savedStatusDuration, m.savedStatusDir, m.homeDir, m.contentWidth()))
	}
	msg := clampWidth(m.statusMsg, m.contentWidth())
	if m.statusIsErr {
		return errStyle.Render(msg)
	}
	return accentStyle.Render(msg)
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
	return fmt.Sprintf("downloading model %s for %s...\n\n%s", m.cfg.WhisperModel, recordDisplayName(m.transcribeTarget), m.downloadProgress.ViewAs(m.downloadPct))
}

func (m Model) transcribingView() string {
	elapsed := time.Since(m.transcribeStart)
	width := m.contentWidth()
	lines := []string{recordDisplayName(m.transcribeTarget), m.transcribeHeadline(elapsed), ""}
	lines = append(lines, wrapLines(m.transcribeLines, width)...)
	return strings.Join(lines, "\n")
}

// transcribeHeadline renders modeTranscribing's first line: whisper-cli's
// own progress bar and percentage once --print-progress has reported in,
// falling back to the spinner (older whisper-cli builds without the flag,
// or simply before the first progress line has arrived).
func (m Model) transcribeHeadline(elapsed time.Duration) string {
	elapsedStr := records.FormatDuration(elapsed.Seconds())
	if m.transcribePhase == transcribePreparing {
		return m.transcribeSpinner.View() + " preparing audio... " + elapsedStr
	}
	if m.transcribeHasPct {
		return fmt.Sprintf("%s  %s", m.transcribeProgress.ViewAs(m.transcribePct), elapsedStr)
	}
	return m.transcribeSpinner.View() + " transcribing... " + elapsedStr
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
//
// A confirmation in flight (delete, transcribe-overwrite, discard-recording)
// takes precedence over everything else: it replaces whatever footer the
// underlying mode would otherwise show, so e.g. "q" isn't advertised as
// "quit" while it's actually cancelling the prompt.
func (m Model) footer() string {
	width := m.contentWidth()
	if m.confirmDelete || m.confirmOverwrite || m.confirmingQuit {
		return clampWidth("y confirm · any other key cancel", width)
	}
	if m.mode == modeList {
		switch {
		case m.list.FilterState() == list.Filtering:
			return clampWidth("enter apply · esc cancel", width)
		case len(m.list.Items()) == 0:
			return clampWidth("r rec · ? help · q/ctrl+c quit", width)
		}
	}
	if m.mode == modeHelp {
		return clampWidth(helpFooter(m.helpViewport), width)
	}
	if entries := footerEntries(m.mode, width < footerCompactThreshold); entries != nil {
		return composeFooter(entries, width)
	}
	return clampWidth(footerFor(m.mode, width < footerCompactThreshold), width)
}

// footerEntries returns modeList/modeDetail's footer as ordered entries,
// nil for every other mode. Compact trades word-abbreviation for fewer
// entries instead (H6): it drops "o finder" and, for the list, "/ filter"
// too, rather than shortening every word into "txs/fndr/flt". o and /
// still show up in the wide footer and in the help overlay.
func footerEntries(mode screen, compact bool) []string {
	switch mode {
	case modeList:
		if compact {
			return []string{"r rec", "enter open", "t transcribe", "d delete", "? help", "q quit"}
		}
		return []string{"r rec", "enter detail", "t transcribe", "o finder", "d delete", "/ filter", "? help", "q/ctrl+c quit"}
	case modeDetail:
		if compact {
			return []string{"t transcribe", "d delete", "esc list"}
		}
		return []string{"t transcribe", "o finder", "d delete", "esc list"}
	default:
		return nil
	}
}

// composeFooter joins entries with " · ", narrowing to width by dropping
// entries from the middle -- never the first or last -- once it doesn't
// fit, instead of a naive right-side clamp that would chop the last entry
// (modeList's quit key) off first.
func composeFooter(entries []string, width int) string {
	for {
		line := strings.Join(entries, " · ")
		if len([]rune(line)) <= width || len(entries) <= 2 {
			return clampWidth(line, width)
		}
		mid := len(entries) / 2
		entries = append(append([]string{}, entries[:mid]...), entries[mid+1:]...)
	}
}

// footerFor renders the static help footer for mode, compact when the
// terminal is narrow. modeList/modeDetail defer to footerEntries (used by
// footer() for actual width-aware composition); this is their plain,
// unclamped join, useful to callers (tests, helpSections) that just want a
// non-empty string.
func footerFor(mode screen, compact bool) string {
	if entries := footerEntries(mode, compact); entries != nil {
		return strings.Join(entries, " · ")
	}
	switch mode {
	case modeRecording:
		return "s/q/ctrl+c stop & save · x discard"
	case modeNameForm:
		return "enter confirm · tab mode · esc/ctrl+c cancel"
	case modeDownloading:
		return "esc/ctrl+c cancel download"
	case modeTranscribing:
		return "esc cancel · ctrl+c cancel"
	case modeTranscribeDownloadConfirm:
		return "y download · any other key cancel"
	case modeRecError, modeTranscribeMissingWhisper, modeTranscribeError:
		return "press any key to continue"
	case modeHelp:
		return "press any key to close"
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

// formatSavedStatus formats the status line shown right after a
// stop-and-save: a checkmark, the recording's duration, and its directory as
// a clickable, home-abbreviated path. The hyperlink's href stays the real
// absolute path -- only the displayed text is abbreviated/truncated -- so a
// shortened, "~"-prefixed label still opens the right place.
func formatSavedStatus(durationSeconds float64, dir, homeDir string, width int) string {
	prefix := "✓ saved " + records.FormatDuration(durationSeconds) + " · "
	avail := width - len([]rune(prefix))
	shown := truncateMiddle(abbreviateHome(dir, homeDir), avail)
	return prefix + hyperlink(shown, dir)
}
