// Package tui implements the bubbletea v2 records list shown by bare
// `nastro` and `nastro records` (without --plain).
package tui

import (
	"fmt"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/scaccogatto/nastro/internal/records"
)

var appStyle = lipgloss.NewStyle().Margin(1, 2)

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

// Model is the bubbletea model for the records list screen.
type Model struct {
	list list.Model
}

// New builds a Model listing recs.
func New(recs []records.Record) Model {
	items := make([]list.Item, len(recs))
	for i, r := range recs {
		items[i] = item{r: r}
	}

	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "nastro records"
	l.SetShowHelp(true)

	return Model{list: l}
}

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		h, v := appStyle.GetFrameSize()
		m.list.SetSize(msg.Width-h, msg.Height-v)
		return m, nil

	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "enter":
			// no detail view yet, v0.2 scope
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m Model) View() tea.View {
	v := tea.NewView(appStyle.Render(m.list.View()))
	v.AltScreen = true
	return v
}

// Run starts the records TUI program.
func Run(recs []records.Record) error {
	_, err := tea.NewProgram(New(recs)).Run()
	return err
}
