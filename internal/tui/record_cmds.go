package tui

import (
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/scaccogatto/nastro/internal/config"
	"github.com/scaccogatto/nastro/internal/record"
	"github.com/scaccogatto/nastro/internal/records"
)

// recStartedMsg is delivered when nastro-tap has been spawned successfully.
type recStartedMsg struct{ sess *record.Session }

// recStartErrMsg is delivered when starting nastro-tap failed outright
// (lock conflict, missing binary, mutually exclusive flags, ...).
type recStartErrMsg struct{ err error }

// recTickMsg is delivered once a second while recording: the audio file's
// current size, from a fresh stat.
type recTickMsg struct{ size int64 }

// tapExitedMsg is delivered exactly once per session, whenever nastro-tap's
// process exits: whether that's because we asked it to (stop/discard) or
// not (crash, TCC permission failure) is tracked in the Model.
type tapExitedMsg struct{ err error }

// killTimeoutMsg is delivered 5s after a stop was requested, in case
// nastro-tap hasn't exited cleanly by then.
type killTimeoutMsg struct{}

// recordsReloadedMsg is delivered after rescanning the output dir, e.g.
// once back on the list screen after a recording ends.
type recordsReloadedMsg struct {
	recs   []records.Record
	status string
	err    error
}

// startRecordingCmd starts a new nastro-tap session (lock, dir, spawn,
// caffeinate -- see record.Start).
func startRecordingCmd(cfg config.Config) tea.Cmd {
	return func() tea.Msg {
		sess, err := record.Start(cfg, record.Options{})
		if err != nil {
			return recStartErrMsg{err: err}
		}
		return recStartedMsg{sess: sess}
	}
}

// awaitOrTickCmd is nastro-tap's session heartbeat: it either reports the
// process having exited, or -- if a second passes first -- the audio file's
// current size, and expects to be re-issued after every recTickMsg. It is
// the sole reader of sess.Wait(), so stopping/killing must go through
// signalCmd/killCmd rather than reading that channel themselves.
func awaitOrTickCmd(sess *record.Session) tea.Cmd {
	return func() tea.Msg {
		select {
		case err := <-sess.Wait():
			return tapExitedMsg{err: err}
		case <-time.After(time.Second):
			info, err := os.Stat(sess.AudioPath)
			var size int64
			if err == nil {
				size = info.Size()
			}
			return recTickMsg{size: size}
		}
	}
}

// signalCmd forwards sig to nastro-tap without waiting for it to exit; the
// exit itself is observed by the already-running awaitOrTickCmd loop.
func signalCmd(sess *record.Session, sig os.Signal) tea.Cmd {
	return func() tea.Msg {
		_ = sess.Signal(sig)
		return nil
	}
}

// killCmd force-stops nastro-tap without waiting for it to exit.
func killCmd(sess *record.Session) tea.Cmd {
	return func() tea.Msg {
		_ = sess.Kill()
		return nil
	}
}

func killTimeoutCmd() tea.Cmd {
	return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return killTimeoutMsg{} })
}

// rescanCmd rescans outputDir for the list screen, carrying a status
// message ("saved <dir>" / "discarded" / "") through to display.
func rescanCmd(outputDir, status string) tea.Cmd {
	return func() tea.Msg {
		recs, err := records.Scan(outputDir)
		return recordsReloadedMsg{recs: recs, status: status, err: err}
	}
}
