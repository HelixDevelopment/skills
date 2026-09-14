package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SearchModel provides interactive search with text input and results list
type SearchModel struct {
	client      *APIClient
	input       textinput.Model
	results     []models.SearchResult
	selectedIdx int
	loading     bool
	searched    bool
	width       int
	height      int
	err         error
	focusInput  bool
}

// NewSearchModel creates a new search model
func NewSearchModel(client *APIClient) SearchModel {
	ti := textinput.New()
	ti.Placeholder = "Search skills..."
	ti.CharLimit = 200
	ti.Width = 50
	ti.PromptStyle = lipgloss.NewStyle().Foreground(colorBlue)
	ti.TextStyle = lipgloss.NewStyle().Foreground(colorFg)
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(colorBlue)
	ti.PlaceholderStyle = lipgloss.NewStyle().Foreground(colorOverlay)

	return SearchModel{
		client:     client,
		input:      ti,
		focusInput: true,
	}
}

// Init implements tea.Model
func (m SearchModel) Init() tea.Cmd {
	return textinput.Blink
}

// searchMsg types
type searchResultsMsg struct {
	results []models.SearchResult
}

type searchErrMsg struct {
	err error
}

// Update implements tea.Model
func (m SearchModel) Update(msg tea.Msg) (SearchModel, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.Width = msg.Width - 6
		return m, nil

	case searchResultsMsg:
		m.loading = false
		m.results = msg.results
		m.searched = true
		m.err = nil
		return m, nil

	case searchErrMsg:
		m.loading = false
		m.err = msg.err
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "tab", "i":
			if !m.focusInput {
				m.focusInput = true
				m.input.Focus()
			} else {
				m.focusInput = false
				m.input.Blur()
			}
			return m, nil

		case "enter":
			if m.focusInput {
				query := m.input.Value()
				if query != "" {
					m.loading = true
					m.searched = false
					m.results = nil
					return m, m.executeSearch(query)
				}
			} else if len(m.results) > 0 {
				// Toggle selection detail
				return m, nil
			}

		case "j", "down":
			if !m.focusInput && len(m.results) > 0 {
				if m.selectedIdx < len(m.results)-1 {
					m.selectedIdx++
				}
			}
			return m, nil

		case "k", "up":
			if !m.focusInput && len(m.results) > 0 {
				if m.selectedIdx > 0 {
					m.selectedIdx--
				}
			}
			return m, nil

		case "ctrl+c":
			if !m.focusInput {
				m.results = nil
				m.searched = false
				m.selectedIdx = 0
			}
			return m, nil

		case "esc":
			if m.focusInput {
				m.focusInput = false
				m.input.Blur()
			}
			return m, nil
		}
	}

	// Pass through to input if focused
	if m.focusInput {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

// View implements tea.Model
func (m SearchModel) View() string {
	var b strings.Builder

	// Search input area
	inputStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		Padding(0, 1)

	if m.focusInput {
		inputStyle = inputStyle.BorderForeground(colorBlue)
	} else {
		inputStyle = inputStyle.BorderForeground(colorOverlay)
	}

	b.WriteString(styleHeader.Render("Search Skills"))
	b.WriteString("\n\n")
	b.WriteString(inputStyle.Render(m.input.View()))
	b.WriteString("\n\n")

	// Results area
	if m.loading {
		b.WriteString(styleWarning.Render("Searching..."))
		return b.String()
	}

	if m.err != nil {
		b.WriteString(styleError.Render("Search error: " + m.err.Error()))
		return b.String()
	}

	if m.searched && len(m.results) == 0 {
		b.WriteString(styleHelp.Render("No results found."))
		return b.String()
	}

	if len(m.results) > 0 {
		b.WriteString(styleInfo.Render(fmt.Sprintf("Results (%d)", len(m.results))))
		b.WriteString("\n\n")

		// Show results
		visibleCount := m.height - 8 // Account for header, input, etc.
		if visibleCount < 1 {
			visibleCount = 1
		}

		startIdx := 0
		if m.selectedIdx >= visibleCount {
			startIdx = m.selectedIdx - visibleCount + 1
		}

		for i := startIdx; i < len(m.results) && i < startIdx+visibleCount; i++ {
			result := m.results[i]
			b.WriteString(m.renderResult(result, i == m.selectedIdx))
			b.WriteString("\n")
		}

		if len(m.results) > visibleCount {
			b.WriteString(styleHelp.Render(fmt.Sprintf("\n... and %d more", len(m.results)-visibleCount)))
		}
	}

	return b.String()
}

// renderResult renders a single search result
func (m SearchModel) renderResult(result models.SearchResult, selected bool) string {
	var b strings.Builder

	nameStyle := lipgloss.NewStyle().Foreground(colorFg).Bold(true)
	titleStyle := lipgloss.NewStyle().Foreground(colorGray)
	scoreStyle := lipgloss.NewStyle().Foreground(colorTeal)

	cursor := "  "
	name := nameStyle.Render(result.Skill.Name)
	title := titleStyle.Render(result.Skill.Title)
	score := scoreStyle.Render(fmt.Sprintf("%.3f", result.Score))

	if selected {
		cursor = "> "
		name = lipgloss.NewStyle().Foreground(colorBlue).Bold(true).Background(colorSurface).Render(result.Skill.Name)
		title = lipgloss.NewStyle().Foreground(colorFg).Background(colorSurface).Render(result.Skill.Title)
		score = lipgloss.NewStyle().Foreground(colorTeal).Background(colorSurface).Render(fmt.Sprintf("%.3f", result.Score))
	}

	b.WriteString(fmt.Sprintf("%s%s %s %s\n", cursor, name, score, title))

	// Description preview
	desc := result.Skill.Description
	if len(desc) > m.width-20 {
		desc = desc[:m.width-20] + "..."
	}
	if desc != "" {
		descStyle := lipgloss.NewStyle().Foreground(colorGray).PaddingLeft(4)
		if selected {
			descStyle = descStyle.Background(colorSurface)
		}
		b.WriteString(descStyle.Render(desc))
	}

	return b.String()
}

// SetSize updates the model dimensions
func (m SearchModel) SetSize(width, height int) tea.Cmd {
	m.width = width
	m.height = height
	m.input.Width = width - 6
	return nil
}

// OnActivate is called when this tab becomes active
func (m SearchModel) OnActivate() tea.Cmd {
	m.focusInput = true
	m.input.Focus()
	return textinput.Blink
}

// Reload forces a data refresh
func (m SearchModel) Reload() tea.Cmd {
	if m.input.Value() != "" {
		m.loading = true
		return m.executeSearch(m.input.Value())
	}
	return nil
}

// executeSearch performs the search API call
func (m SearchModel) executeSearch(query string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		results, err := m.client.Search(ctx, query, 50)
		if err != nil {
			return searchErrMsg{err: err}
		}
		return searchResultsMsg{results: results}
	}
}
