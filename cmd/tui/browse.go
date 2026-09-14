package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/HelixDevelopment/skills/internal/models"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// BrowseModel shows a paginated list of skills with detail view
type BrowseModel struct {
	client     *APIClient
	list       list.Model
	skills     []models.Skill
	selected   *models.Skill
	detailView bool
	loading    bool
	width      int
	height     int
	filterMode bool
	err        error
}

// SkillItem wraps a skill for the list component
type SkillItem struct {
	skill models.Skill
}

// FilterValue implements list.Item
func (i SkillItem) FilterValue() string {
	return i.skill.Name + " " + i.skill.Title + " " + string(i.skill.Status)
}

// Title returns the item title for the list
func (i SkillItem) Title() string {
	return i.skill.Name
}

// Description returns the item description
func (i SkillItem) Description() string {
	statusColor := ""
	switch i.skill.Status {
	case models.SkillStatusActive:
		statusColor = "+"
	case models.SkillStatusDraft:
		statusColor = "~"
	case models.SkillStatusValidated:
		statusColor = "✓"
	case models.SkillStatusDeprecated:
		statusColor = "×"
	}
	return fmt.Sprintf("[%s] %s | v%s", statusColor, i.skill.Title, i.skill.Version)
}

// skillDelegate customizes the list item rendering
type skillDelegate struct{}

func (d skillDelegate) Height() int                               { return 2 }
func (d skillDelegate) Spacing() int                              { return 1 }
func (d skillDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd { return nil }
func (d skillDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	si, ok := item.(SkillItem)
	if !ok {
		return
	}

	nameStyle := lipgloss.NewStyle().Foreground(colorFg).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(colorGray)
	selectedNameStyle := lipgloss.NewStyle().Foreground(colorBlue).Bold(true).Background(colorSurface)
	selectedDescStyle := lipgloss.NewStyle().Foreground(colorFg).Background(colorSurface)

	cursor := "  "
	name := nameStyle.Render(si.skill.Name)
	desc := descStyle.Render(si.Description())

	if index == m.Index() {
		cursor = "> "
		name = selectedNameStyle.Render(si.skill.Name)
		desc = selectedDescStyle.Render(si.Description())
	}

	fmt.Fprintf(w, "%s%s\n%s %s\n", cursor, name, strings.Repeat(" ", lipgloss.Width(cursor)), desc)
}

// NewBrowseModel creates a new browse model
func NewBrowseModel(client *APIClient) BrowseModel {
	// Create list with default dimensions; will be resized
	l := list.New([]list.Item{}, skillDelegate{}, 80, 20)
	l.Title = "Skills"
	l.SetShowStatusBar(true)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	l.Styles.Title = styleTitle
	l.Styles.StatusBar = lipgloss.NewStyle().Foreground(colorGray)
	l.Styles.FilterPrompt = lipgloss.NewStyle().Foreground(colorBlue)
	l.Styles.FilterCursor = lipgloss.NewStyle().Foreground(colorBlue)
	l.KeyMap.Quit.Unbind()
	l.KeyMap.CancelWhileFiltering.Unbind()

	return BrowseModel{
		client: client,
		list:   l,
	}
}

// Init implements tea.Model
func (m BrowseModel) Init() tea.Cmd {
	return m.loadSkills()
}

// loadSkills fetches skills from the API
func (m BrowseModel) loadSkills() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		skills, err := m.client.ListSkills(ctx)
		if err != nil {
			return browseErrMsg{err: err}
		}
		return browseSkillsLoadedMsg{skills: skills}
	}
}

// browseMsg types for Update
type browseSkillsLoadedMsg struct {
	skills []models.Skill
}

type browseErrMsg struct {
	err error
}

type browseSkillDetailMsg struct {
	skill *models.Skill
}

// Update implements tea.Model
func (m BrowseModel) Update(msg tea.Msg) (BrowseModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.list.SetSize(msg.Width, msg.Height)
		return m, nil

	case browseSkillsLoadedMsg:
		m.loading = false
		m.skills = msg.skills
		m.err = nil

		// Convert skills to list items
		items := make([]list.Item, len(msg.skills))
		for i, s := range msg.skills {
			items[i] = SkillItem{skill: s}
		}
		return m, m.list.SetItems(items)

	case browseErrMsg:
		m.loading = false
		m.err = msg.err
		return m, nil

	case browseSkillDetailMsg:
		m.selected = msg.skill
		m.detailView = true
		return m, nil

	case tea.KeyMsg:
		// Handle detail view
		if m.detailView {
			switch msg.String() {
			case "esc", "q", "backspace":
				m.detailView = false
				m.selected = nil
				return m, nil
			}
			return m, nil
		}

		// Normal list navigation
		switch msg.String() {
		case "enter":
			if item, ok := m.list.SelectedItem().(SkillItem); ok {
				return m, m.loadSkillDetail(item.skill.Name)
			}
		case "/":
			m.filterMode = true
			m.list.SetFilteringEnabled(true)
		case "esc":
			if m.filterMode {
				m.filterMode = false
			} else {
				m.list.ResetFilter()
			}
		case "r":
			m.loading = true
			return m, m.loadSkills()
		}
	}

	// Pass through to list
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// View implements tea.Model
func (m BrowseModel) View() string {
	if m.loading {
		return m.renderLoading()
	}

	if m.err != nil {
		return m.renderError()
	}

	if m.detailView && m.selected != nil {
		return m.renderDetail()
	}

	return m.list.View()
}

// SetSize updates the model dimensions
func (m BrowseModel) SetSize(width, height int) tea.Cmd {
	m.width = width
	m.height = height
	m.list.SetSize(width, height)
	return nil
}

// OnActivate is called when this tab becomes active
func (m BrowseModel) OnActivate() tea.Cmd {
	if len(m.skills) == 0 {
		return m.loadSkills()
	}
	return nil
}

// Reload forces a data refresh
func (m BrowseModel) Reload() tea.Cmd {
	m.loading = true
	return m.loadSkills()
}

// loadSkillDetail fetches a single skill
func (m BrowseModel) loadSkillDetail(name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		skill, err := m.client.GetSkill(ctx, name)
		if err != nil {
			return browseErrMsg{err: err}
		}
		return browseSkillDetailMsg{skill: skill}
	}
}

// renderLoading shows a loading spinner
func (m BrowseModel) renderLoading() string {
	s := styleWarning.Render("⟳ Loading skills...")
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, s)
}

// renderError shows an error message
func (m BrowseModel) renderError() string {
	msg := styleError.Render("Error loading skills: " + m.err.Error())
	help := styleHelp.Render("\n\nPress 'r' to retry")
	content := msg + help
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
}

// renderDetail shows skill detail view
func (m BrowseModel) renderDetail() string {
	s := m.selected
	if s == nil {
		return ""
	}

	var b strings.Builder

	// Header
	b.WriteString(styleHeader.Render(s.Name))
	b.WriteString("\n")
	b.WriteString(styleTitle.Render(s.Title))
	b.WriteString("\n\n")

	// Status badge
	statusStyle := lipgloss.NewStyle().
		Padding(0, 1).
		Bold(true)
	switch s.Status {
	case models.SkillStatusActive:
		statusStyle = statusStyle.Background(lipgloss.Color("#2d4a3e")).Foreground(colorGreen)
	case models.SkillStatusDraft:
		statusStyle = statusStyle.Background(lipgloss.Color("#4a442d")).Foreground(colorYellow)
	case models.SkillStatusValidated:
		statusStyle = statusStyle.Background(lipgloss.Color("#2d3a4a")).Foreground(colorBlue)
	case models.SkillStatusDeprecated:
		statusStyle = statusStyle.Background(lipgloss.Color("#4a2d2d")).Foreground(colorRed)
	}
	b.WriteString(statusStyle.Render(string(s.Status)))
	b.WriteString("  ")
	b.WriteString(lipgloss.NewStyle().Foreground(colorGray).Render("v" + s.Version))
	b.WriteString("\n\n")

	// Description
	if s.Description != "" {
		b.WriteString(styleInfo.Render("Description") + "\n")
		b.WriteString(s.Description)
		b.WriteString("\n\n")
	}

	// Content preview
	if s.Content != "" {
		b.WriteString(styleInfo.Render("Content") + "\n")
		content := s.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		b.WriteString(content)
		b.WriteString("\n\n")
	}

	// Dependencies
	if len(s.Dependencies) > 0 {
		b.WriteString(styleInfo.Render(fmt.Sprintf("Dependencies (%d)", len(s.Dependencies))) + "\n")
		for _, dep := range s.Dependencies {
			relColor := colorGray
			switch dep.RelationType {
			case models.DepTypeRequires:
				relColor = colorRed
			case models.DepTypeExtends:
				relColor = colorBlue
			case models.DepTypeRecommends:
				relColor = colorGreen
			}
			relStyle := lipgloss.NewStyle().Foreground(relColor)
			b.WriteString(fmt.Sprintf("  %s %s\n", relStyle.Render(string(dep.RelationType)), dep.DependsOnName))
		}
		b.WriteString("\n")
	}

	// Resources
	if len(s.Resources) > 0 {
		b.WriteString(styleInfo.Render(fmt.Sprintf("Resources (%d)", len(s.Resources))) + "\n")
		for _, r := range s.Resources {
			b.WriteString(fmt.Sprintf("  [%s] %s - %s\n", r.ResourceType, r.Title, r.URL))
		}
		b.WriteString("\n")
	}

	// Metadata
	b.WriteString(styleInfo.Render("Metadata") + "\n")
	b.WriteString(fmt.Sprintf("  ID:        %s\n", s.ID))
	b.WriteString(fmt.Sprintf("  Created:   %s\n", s.CreatedAt.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("  Updated:   %s\n", s.UpdatedAt.Format(time.RFC3339)))

	// Footer
	b.WriteString("\n")
	b.WriteString(styleHelp.Render("Press Esc or q to go back"))

	return b.String()
}
