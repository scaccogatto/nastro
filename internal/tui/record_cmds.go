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
// diskWarning carries the output dir's low-disk-space warning, if any (see
// record.Session.LowDiskWarning): the TUI shows it inline rather than
// printing straight to stderr like the CLI does.
type recStartedMsg struct {
	sess        *record.Session
	diskWarning string
}

// recStartErrMsg is delivered when starting nastro-tap failed outright
// (lock conflict, missing binary, mutually exclusive flags, ...).
type recStartErrMsg struct{ err error }

// recTickMsg is delivered once a second while recording: the audio file's
// current size, from a fresh stat.
type recTickMsg struct{ size int64 }

// levelMsg carries one second's worth of parsed VU-meter levels from
// nastro-tap's stdout (see record.Session.Levels).
type levelMsg struct{ lvl record.Level }

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
	isErr  bool
	err    error
}

// startRecordingCmd starts a new nastro-tap session (lock, dir, spawn,
// caffeinate -- see record.Start).
func startRecordingCmd(cfg config.Config, opts record.Options) tea.Cmd {
	return func() tea.Msg {
		sess, err := record.Start(cfg, opts)
		if err != nil {
			return recStartErrMsg{err: err}
		}
		return recStartedMsg{sess: sess, diskWarning: sess.LowDiskWarning}
	}
}

// awaitCmd is nastro-tap's process/level heartbeat: it reports either the
// process having exited or a parsed VU-meter level, and expects to be
// re-issued after every levelMsg (not after tapExitedMsg, which ends the
// session). It is the sole reader of sess.Wait() and sess.Levels(), so
// stopping/killing must go through signalCmd/killCmd rather than reading
// those channels themselves.
//
// Deliberately separate from sizeTickCmd: the two used to be one loop that
// re-armed a single time.After(1s) on every message, so a VU-meter stream
// at or above 1Hz (nastro-tap's normal rate) kept resetting the timer and
// starved the on-disk size refresh. Splitting them means the size tick
// fires on its own schedule no matter how chatty the levels are.
func awaitCmd(sess *record.Session) tea.Cmd {
	return func() tea.Msg {
		for {
			select {
			case err := <-sess.Wait():
				return tapExitedMsg{err: err}
			case lvl, ok := <-sess.Levels():
				if !ok {
					continue // closed right as the tap exits; loop picks that up
				}
				return levelMsg{lvl: lvl}
			}
		}
	}
}

// sizeTickCmd is the recording screen's independent once-a-second heartbeat
// for the audio file's on-disk size (see awaitCmd's doc comment for why it's
// not part of that loop).
func sizeTickCmd(sess *record.Session) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg {
		info, err := os.Stat(sess.AudioPath)
		var size int64
		if err == nil {
			size = info.Size()
		}
		return recTickMsg{size: size}
	})
}

// signalCmd forwards sig to nastro-tap without waiting for it to exit; the
// exit itself is observed by the already-running awaitCmd loop.
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
// message ("saved <dir>" / "discarded" / "deleted <id>" / "") and its
// severity (error-styled vs. ok-styled) through to display.
func rescanCmd(outputDir, status string, isErr bool) tea.Cmd {
	return func() tea.Msg {
		recs, err := records.Scan(outputDir)
		return recordsReloadedMsg{recs: recs, status: status, isErr: isErr, err: err}
	}
}
