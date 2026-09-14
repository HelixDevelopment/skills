package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// RegistryStats holds computed registry statistics
type RegistryStats struct {
	TotalSkills    int
	Coverage       float64
	MissingDeps    int
	StaleSkills    int
	SkillsByStatus map[string]int
	Health         string
}

// RegistryModel shows registry health status
type RegistryModel struct {
	client     *APIClient
	entries    []SkillRegistryEntry
	stats      *RegistryStats
	loading    bool
	err        error
	width      int
	height     int
	activeView int // 0=overview, 1=missing, 2=stale, 3=entries
	focusIdx   int
}

// SkillRegistryEntry is a local copy of the model to avoid import cycles
type SkillRegistryEntry struct {
	SkillName   string     `json:"skill_name"`
	MissingDeps []string   `json:"missing_deps"`
	Stale       bool       `json:"stale"`
	LastReview  *time.Time `json:"last_review"`
	AutoExpand  bool       `json:"auto_expand"`
	Coverage    float64    `json:"coverage"`
}

// registryMsg types
type registryLoadedMsg struct {
	entries []SkillRegistryEntry
	stats   *RegistryStats
}

type registryErrMsg struct {
	err error
}

// NewRegistryModel creates a new registry model
func NewRegistryModel(client *APIClient) RegistryModel {
	return RegistryModel{
		client: client,
	}
}

// Init implements tea.Model
func (m RegistryModel) Init() tea.Cmd {
	return m.loadRegistry()
}

// Update implements tea.Model
func (m RegistryModel) Update(msg tea.Msg) (RegistryModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case registryLoadedMsg:
		m.loading = false
		m.entries = msg.entries
		m.stats = msg.stats
		m.err = nil
		return m, nil

	case registryErrMsg:
		m.loading = false
		m.err = msg.err
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "1":
			m.activeView = 0
			m.focusIdx = 0
		case "2":
			m.activeView = 1
			m.focusIdx = 0
		case "3":
			m.activeView = 2
			m.focusIdx = 0
		case "4":
			m.activeView = 3
			m.focusIdx = 0

		case "j", "down":
			maxIdx := m.maxFocusIdx()
			if m.focusIdx < maxIdx {
				m.focusIdx++
			}
		case "k", "up":
			if m.focusIdx > 0 {
				m.focusIdx--
			}
		case "r":
			m.loading = true
			return m, m.loadRegistry()
		}
	}

	return m, nil
}

// View implements tea.Model
func (m RegistryModel) View() string {
	var b strings.Builder

	b.WriteString(styleHeader.Render("Registry Dashboard"))
	b.WriteString("\n\n")

	if m.loading {
		b.WriteString(styleWarning.Render("⟳ Loading registry data..."))
		return b.String()
	}

	if m.err != nil {
		b.WriteString(styleError.Render("Error: " + m.err.Error()))
		b.WriteString("\n\n")
		b.WriteString(styleHelp.Render("Press 'r' to retry"))
		return b.String()
	}

	// View tabs
	b.WriteString(m.renderViewTabs())
	b.WriteString("\n\n")

	// Render active view
	switch m.activeView {
	case 0:
		b.WriteString(m.renderOverview())
	case 1:
		b.WriteString(m.renderMissingDeps())
	case 2:
		b.WriteString(m.renderStaleSkills())
	case 3:
		b.WriteString(m.renderAllEntries())
	}

	return b.String()
}

// renderViewTabs renders the sub-view navigation
func (m RegistryModel) renderViewTabs() string {
	views := []string{"Overview [1]", "Missing Deps [2]", "Stale Skills [3]", "All Entries [4]"}
	var parts []string

	for i, v := range views {
		if i == m.activeView {
			parts = append(parts, styleTabActive.Render(v))
		} else {
			parts = append(parts, styleTab.Render(v))
		}
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

// renderOverview shows the registry overview with stats and coverage
func (m RegistryModel) renderOverview() string {
	var b strings.Builder

	if m.stats == nil {
		b.WriteString(styleHelp.Render("No statistics available."))
		return b.String()
	}

	s := m.stats

	// Health indicator
	healthColor := colorGreen
	healthBg := lipgloss.Color("#2d4a3e")
	switch s.Health {
	case "critical":
		healthColor = colorRed
		healthBg = lipgloss.Color("#4a2d2d")
	case "warning":
		healthColor = colorYellow
		healthBg = lipgloss.Color("#4a442d")
	}

	healthBadge := lipgloss.NewStyle().
		Background(healthBg).
		Foreground(healthColor).
		Bold(true).
		Padding(0, 2).
		Render(strings.ToUpper(s.Health))

	b.WriteString(fmt.Sprintf("Health Status: %s\n\n", healthBadge))

	// Key metrics
	b.WriteString(styleInfo.Render("Key Metrics"))
	b.WriteString("\n")

	metrics := []struct {
		label string
		value int
		color lipgloss.Color
	}{
		{"Total Skills", s.TotalSkills, colorFg},
		{"Missing Deps", s.MissingDeps, colorRed},
		{"Stale Skills", s.StaleSkills, colorYellow},
	}

	for _, m := range metrics {
		valStyle := lipgloss.NewStyle().Foreground(m.color).Bold(true)
		labelStyle := lipgloss.NewStyle().Foreground(colorGray)
		b.WriteString(fmt.Sprintf("  %-15s %s\n", labelStyle.Render(m.label), valStyle.Render(fmt.Sprintf("%d", m.value))))
	}

	// Coverage
	b.WriteString("\n")
	b.WriteString(styleInfo.Render("Overall Coverage"))
	b.WriteString("\n")
	b.WriteString(m.renderProgressBar(s.Coverage, 40))
	b.WriteString(fmt.Sprintf(" %.1f%%\n", s.Coverage*100))

	// Skills by status
	if len(s.SkillsByStatus) > 0 {
		b.WriteString("\n")
		b.WriteString(styleInfo.Render("Skills by Status"))
		b.WriteString("\n")
		for status, count := range s.SkillsByStatus {
			b.WriteString(fmt.Sprintf("  %-12s %d\n", status, count))
		}
	}

	return b.String()
}

// renderMissingDeps shows skills with missing dependencies
func (m RegistryModel) renderMissingDeps() string {
	var b strings.Builder

	// Filter entries with missing deps
	var missing []SkillRegistryEntry
	for _, e := range m.entries {
		if len(e.MissingDeps) > 0 {
			missing = append(missing, e)
		}
	}

	if len(missing) == 0 {
		b.WriteString(styleSuccess.Render("All dependencies resolved! No missing dependencies found."))
		return b.String()
	}

	b.WriteString(fmt.Sprintf("Skills with Missing Dependencies (%d)\n\n", len(missing)))

	// Header
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(colorBlue)
	b.WriteString(fmt.Sprintf("  %-25s %s\n", headerStyle.Render("Skill"), headerStyle.Render("Missing Dependencies")))
	b.WriteString(strings.Repeat("-", m.width-4))
	b.WriteString("\n")

	// Visible range
	visibleCount := m.height - 12
	if visibleCount < 1 {
		visibleCount = 1
	}
	startIdx := 0
	if m.focusIdx >= visibleCount {
		startIdx = m.focusIdx - visibleCount + 1
	}

	for i := startIdx; i < len(missing) && i < startIdx+visibleCount; i++ {
		e := missing[i]
		nameStyle := lipgloss.NewStyle().Foreground(colorFg)
		if i == m.focusIdx {
			nameStyle = lipgloss.NewStyle().Foreground(colorBlue).Bold(true).Background(colorSurface)
		}
		b.WriteString(fmt.Sprintf("  %s\n", nameStyle.Render(e.SkillName)))
		for _, dep := range e.MissingDeps {
			b.WriteString(fmt.Sprintf("    %s %s\n", lipgloss.NewStyle().Foreground(colorRed).Render("×"), dep))
		}
	}

	if len(missing) > visibleCount {
		b.WriteString(styleHelp.Render(fmt.Sprintf("\n... and %d more", len(missing)-visibleCount)))
	}

	return b.String()
}

// renderStaleSkills shows stale skills
func (m RegistryModel) renderStaleSkills() string {
	var b strings.Builder

	// Filter stale entries
	var stale []SkillRegistryEntry
	for _, e := range m.entries {
		if e.Stale {
			stale = append(stale, e)
		}
	}

	if len(stale) == 0 {
		b.WriteString(styleSuccess.Render("No stale skills found. Registry is up to date!"))
		return b.String()
	}

	b.WriteString(fmt.Sprintf("Stale Skills (%d)\n\n", len(stale)))

	// Header
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(colorBlue)
	b.WriteString(fmt.Sprintf("  %-25s %-15s %s\n",
		headerStyle.Render("Skill"),
		headerStyle.Render("Last Review"),
		headerStyle.Render("Coverage")))
	b.WriteString(strings.Repeat("-", m.width-4))
	b.WriteString("\n")

	// Visible range
	visibleCount := m.height - 12
	if visibleCount < 1 {
		visibleCount = 1
	}
	startIdx := 0
	if m.focusIdx >= visibleCount {
		startIdx = m.focusIdx - visibleCount + 1
	}

	for i := startIdx; i < len(stale) && i < startIdx+visibleCount; i++ {
		e := stale[i]
		nameStyle := lipgloss.NewStyle().Foreground(colorFg)
		if i == m.focusIdx {
			nameStyle = lipgloss.NewStyle().Foreground(colorBlue).Bold(true).Background(colorSurface)
		}

		lastReview := "never"
		if e.LastReview != nil {
			lastReview = e.LastReview.Format("2006-01-02")
		}

		covStr := fmt.Sprintf("%.0f%%", e.Coverage*100)
		covColor := colorGreen
		if e.Coverage < 0.5 {
			covColor = colorRed
		} else if e.Coverage < 0.8 {
			covColor = colorYellow
		}
		covStyle := lipgloss.NewStyle().Foreground(covColor)

		b.WriteString(fmt.Sprintf("  %-25s %-15s %s\n",
			nameStyle.Render(e.SkillName),
			lastReview,
			covStyle.Render(covStr)))
	}

	if len(stale) > visibleCount {
		b.WriteString(styleHelp.Render(fmt.Sprintf("\n... and %d more", len(stale)-visibleCount)))
	}

	return b.String()
}

// renderAllEntries shows all registry entries
func (m RegistryModel) renderAllEntries() string {
	var b strings.Builder

	if len(m.entries) == 0 {
		b.WriteString(styleHelp.Render("No registry entries found."))
		return b.String()
	}

	b.WriteString(fmt.Sprintf("All Registry Entries (%d)\n\n", len(m.entries)))

	// Header
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(colorBlue)
	b.WriteString(fmt.Sprintf("  %-25s %-8s %-12s %s\n",
		headerStyle.Render("Skill"),
		headerStyle.Render("Stale"),
		headerStyle.Render("Coverage"),
		headerStyle.Render("Missing Deps")))
	b.WriteString(strings.Repeat("-", m.width-4))
	b.WriteString("\n")

	// Visible range
	visibleCount := m.height - 12
	if visibleCount < 1 {
		visibleCount = 1
	}
	startIdx := 0
	if m.focusIdx >= visibleCount {
		startIdx = m.focusIdx - visibleCount + 1
	}

	for i := startIdx; i < len(m.entries) && i < startIdx+visibleCount; i++ {
		e := m.entries[i]
		nameStyle := lipgloss.NewStyle().Foreground(colorFg)
		if i == m.focusIdx {
			nameStyle = lipgloss.NewStyle().Foreground(colorBlue).Bold(true).Background(colorSurface)
		}

		staleStr := "no"
		staleColor := colorGreen
		if e.Stale {
			staleStr = "yes"
			staleColor = colorYellow
		}

		covStr := fmt.Sprintf("%.0f%%", e.Coverage*100)
		covColor := colorGreen
		if e.Coverage < 0.5 {
			covColor = colorRed
		} else if e.Coverage < 0.8 {
			covColor = colorYellow
		}

		missingCount := fmt.Sprintf("%d", len(e.MissingDeps))
		missingColor := colorGreen
		if len(e.MissingDeps) > 0 {
			missingColor = colorRed
		}

		b.WriteString(fmt.Sprintf("  %-25s %-8s %-12s %s\n",
			nameStyle.Render(e.SkillName),
			lipgloss.NewStyle().Foreground(staleColor).Render(staleStr),
			lipgloss.NewStyle().Foreground(covColor).Render(covStr),
			lipgloss.NewStyle().Foreground(missingColor).Render(missingCount)))
	}

	if len(m.entries) > visibleCount {
		b.WriteString(styleHelp.Render(fmt.Sprintf("\n... and %d more", len(m.entries)-visibleCount)))
	}

	return b.String()
}

// renderProgressBar creates an ASCII progress bar
func (m RegistryModel) renderProgressBar(percentage float64, width int) string {
	filled := int(percentage * float64(width))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	empty := width - filled

	var barColor lipgloss.Color
	if percentage >= 0.8 {
		barColor = colorGreen
	} else if percentage >= 0.5 {
		barColor = colorYellow
	} else {
		barColor = colorRed
	}

	filledStr := lipgloss.NewStyle().Foreground(barColor).Background(barColor).Render(strings.Repeat("█", filled))
	emptyStr := lipgloss.NewStyle().Foreground(colorOverlay).Render(strings.Repeat("░", empty))

	return filledStr + emptyStr
}

// maxFocusIdx returns the maximum focusable index for the current view
func (m RegistryModel) maxFocusIdx() int {
	switch m.activeView {
	case 1: // missing
		count := 0
		for _, e := range m.entries {
			if len(e.MissingDeps) > 0 {
				count++
			}
		}
		return count - 1
	case 2: // stale
		count := 0
		for _, e := range m.entries {
			if e.Stale {
				count++
			}
		}
		return count - 1
	case 3: // all
		return len(m.entries) - 1
	default:
		return 0
	}
}

// loadRegistry fetches registry data from the API
func (m RegistryModel) loadRegistry() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		// Try to get stats first
		stats, err := m.client.GetRegistryStats(ctx)
		if err != nil {
			// Continue with just entries
			stats = nil
		}

		// Get raw entries
		entries, err := m.client.GetRegistry(ctx)
		if err != nil {
			return registryErrMsg{err: err}
		}

		// Convert to local type
		localEntries := make([]SkillRegistryEntry, len(entries))
		for i, e := range entries {
			var lastReview *time.Time
			if e.LastReview != nil {
				t := *e.LastReview
				lastReview = &t
			}
			localEntries[i] = SkillRegistryEntry{
				SkillName:   e.SkillName,
				MissingDeps: e.MissingDeps,
				Stale:       e.Stale,
				LastReview:  lastReview,
				AutoExpand:  e.AutoExpand,
				Coverage:    e.Coverage,
			}
		}

		return registryLoadedMsg{
			entries: localEntries,
			stats:   stats,
		}
	}
}

// SetSize updates the model dimensions
func (m RegistryModel) SetSize(width, height int) tea.Cmd {
	m.width = width
	m.height = height
	return nil
}

// OnActivate is called when this tab becomes active
func (m RegistryModel) OnActivate() tea.Cmd {
	if m.stats == nil && !m.loading {
		m.loading = true
		return m.loadRegistry()
	}
	return nil
}

// Reload forces a data refresh
func (m RegistryModel) Reload() tea.Cmd {
	m.loading = true
	return m.loadRegistry()
}
