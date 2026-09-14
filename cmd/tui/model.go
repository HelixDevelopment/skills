package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Tab identifiers
const (
	tabBrowse = iota
	tabSearch
	tabTree
	tabRegistry
)

// Tab names
var tabNames = []string{" Browse ", " Search ", " Tree View ", " Registry "}

// Tab short help
var tabHelp = []string{"F1", "F2", "F3", "F4"}

// KeyMap defines the full set of keyboard shortcuts
type KeyMap struct {
	Quit    key.Binding
	Help    key.Binding
	NextTab key.Binding
	PrevTab key.Binding
	Tab1    key.Binding
	Tab2    key.Binding
	Tab3    key.Binding
	Tab4    key.Binding
	Reload  key.Binding
}

// DefaultKeyMap returns the default key bindings
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Quit: key.NewBinding(
			key.WithKeys("ctrl+c", "q"),
			key.WithHelp("q/ctrl+c", "quit"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "help"),
		),
		NextTab: key.NewBinding(
			key.WithKeys("tab", "right", "l"),
			key.WithHelp("tab/→/l", "next tab"),
		),
		PrevTab: key.NewBinding(
			key.WithKeys("shift+tab", "left", "h"),
			key.WithHelp("shift+tab/←/h", "prev tab"),
		),
		Tab1: key.NewBinding(
			key.WithKeys("f1"),
			key.WithHelp("F1", "browse"),
		),
		Tab2: key.NewBinding(
			key.WithKeys("f2"),
			key.WithHelp("F2", "search"),
		),
		Tab3: key.NewBinding(
			key.WithKeys("f3"),
			key.WithHelp("F3", "tree view"),
		),
		Tab4: key.NewBinding(
			key.WithKeys("f4"),
			key.WithHelp("F4", "registry"),
		),
		Reload: key.NewBinding(
			key.WithKeys("ctrl+r", "r"),
			key.WithHelp("ctrl+r/r", "reload"),
		),
	}
}

// Model is the root Bubble Tea model managing all tabs
type Model struct {
	// Tab state
	tabs      []string
	activeTab int
	width     int
	height    int

	// Sub-models for each tab
	browseModel   BrowseModel
	searchModel   SearchModel
	treeModel     TreeModel
	registryModel RegistryModel

	// Shared
	apiClient   *APIClient
	keys        KeyMap
	helpOpen    bool
	showMessage string
	msgType     string // "error" | "success" | "warning"

	// Connection state
	connected bool
}

// NewModel creates the initial TUI model
func NewModel(client *APIClient, connected bool) Model {
	m := Model{
		tabs:      tabNames,
		activeTab: tabBrowse,
		apiClient: client,
		keys:      DefaultKeyMap(),
		connected: connected,
		helpOpen:  false,
	}

	// Initialize sub-models
	m.browseModel = NewBrowseModel(client)
	m.searchModel = NewSearchModel(client)
	m.treeModel = NewTreeModel(client)
	m.registryModel = NewRegistryModel(client)

	return m
}

// Init implements tea.Model
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.browseModel.Init(),
		m.searchModel.Init(),
		m.treeModel.Init(),
		m.registryModel.Init(),
	)
}

// Update implements tea.Model
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		// Pass window size to all sub-models
		bwm, bcmd := m.browseModel.Update(msg)
		m.browseModel = bwm
		swm, scmd := m.searchModel.Update(msg)
		m.searchModel = swm
		twm, tcmd := m.treeModel.Update(msg)
		m.treeModel = twm
		rwm, rcmd := m.registryModel.Update(msg)
		m.registryModel = rwm

		cmds = append(cmds, bcmd, scmd, tcmd, rcmd)

	case tea.KeyMsg:
		// Help overlay toggle
		if key.Matches(msg, m.keys.Help) {
			m.helpOpen = !m.helpOpen
			return m, tea.Batch(cmds...)
		}

		// Quit
		if key.Matches(msg, m.keys.Quit) && !m.helpOpen {
			return m, tea.Quit
		}

		// Tab navigation (only when help is closed)
		if !m.helpOpen {
			switch {
			case key.Matches(msg, m.keys.NextTab):
				m.activeTab = (m.activeTab + 1) % len(m.tabs)
				return m, m.onTabChange()
			case key.Matches(msg, m.keys.PrevTab):
				m.activeTab = (m.activeTab - 1 + len(m.tabs)) % len(m.tabs)
				return m, m.onTabChange()
			case key.Matches(msg, m.keys.Tab1):
				m.activeTab = tabBrowse
				return m, m.onTabChange()
			case key.Matches(msg, m.keys.Tab2):
				m.activeTab = tabSearch
				return m, m.onTabChange()
			case key.Matches(msg, m.keys.Tab3):
				m.activeTab = tabTree
				return m, m.onTabChange()
			case key.Matches(msg, m.keys.Tab4):
				m.activeTab = tabRegistry
				return m, m.onTabChange()
			case key.Matches(msg, m.keys.Reload):
				return m, m.onReload()
			}
		}
	}

	// Route messages to active sub-model (unless help is open)
	if !m.helpOpen {
		switch m.activeTab {
		case tabBrowse:
			bm, cmd := m.browseModel.Update(msg)
			m.browseModel = bm
			cmds = append(cmds, cmd)
		case tabSearch:
			sm, cmd := m.searchModel.Update(msg)
			m.searchModel = sm
			cmds = append(cmds, cmd)
		case tabTree:
			tm, cmd := m.treeModel.Update(msg)
			m.treeModel = tm
			cmds = append(cmds, cmd)
		case tabRegistry:
			rm, cmd := m.registryModel.Update(msg)
			m.registryModel = rm
			cmds = append(cmds, cmd)
		}
	}

	return m, tea.Batch(cmds...)
}

// View implements tea.Model
func (m Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "Loading..."
	}

	var b strings.Builder

	// Title bar
	b.WriteString(m.renderTitleBar())
	b.WriteString("\n")

	// Tab bar
	b.WriteString(m.renderTabs())
	b.WriteString("\n")

	// Content area
	contentHeight := m.contentHeight()
	content := m.renderContent()
	// Pad or trim content to fit
	contentLines := strings.Split(content, "\n")
	for i := 0; i < contentHeight && i < len(contentLines); i++ {
		b.WriteString(contentLines[i])
		b.WriteString("\n")
	}
	// Fill remaining lines
	for i := len(contentLines); i < contentHeight; i++ {
		b.WriteString("\n")
	}

	// Message bar (error/success messages)
	if m.showMessage != "" {
		b.WriteString(m.renderMessage())
		b.WriteString("\n")
	}

	// Status bar
	b.WriteString(m.renderStatusBar())

	// Help overlay
	if m.helpOpen {
		help := m.renderHelp()
		// Overlay help on top of current view
		return m.overlayHelp(b.String(), help)
	}

	return b.String()
}

// renderTitleBar renders the application title
func (m Model) renderTitleBar() string {
	title := "HelixKnowledge Skill Graph"
	version := "v" + appVersion

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(colorBlue).
		Padding(0, 1)

	versionStyle := lipgloss.NewStyle().
		Foreground(colorGray).
		Padding(0, 1)

	connStyle := lipgloss.NewStyle().
		Foreground(colorGreen).
		Padding(0, 1)
	if !m.connected {
		connStyle = connStyle.Foreground(colorRed)
		connStyle = lipgloss.NewStyle().
			Foreground(colorRed).
			Padding(0, 1)
	}

	connStatus := "connected"
	if !m.connected {
		connStatus = "disconnected"
	}

	left := titleStyle.Render(title)
	right := versionStyle.Render(version) + " " + connStyle.Render(connStatus)

	// Fill space between
	fillWidth := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if fillWidth < 0 {
		fillWidth = 0
	}

	return left + strings.Repeat(" ", fillWidth) + right
}

// renderTabs renders the tab navigation bar
func (m Model) renderTabs() string {
	var tabs []string
	for i, name := range m.tabs {
		if i == m.activeTab {
			tabs = append(tabs, styleTabActive.Render(name))
		} else {
			tabs = append(tabs, styleTab.Render(name))
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
}

// renderContent renders the active tab's content
func (m Model) renderContent() string {
	switch m.activeTab {
	case tabBrowse:
		return m.browseModel.View()
	case tabSearch:
		return m.searchModel.View()
	case tabTree:
		return m.treeModel.View()
	case tabRegistry:
		return m.registryModel.View()
	default:
		return "Unknown tab"
	}
}

// renderMessage renders a message bar
func (m Model) renderMessage() string {
	var msgStyle lipgloss.Style
	switch m.msgType {
	case "error":
		msgStyle = styleError
	case "success":
		msgStyle = styleSuccess
	case "warning":
		msgStyle = styleWarning
	default:
		msgStyle = styleInfo
	}
	return msgStyle.Render(m.showMessage)
}

// renderStatusBar renders the bottom status bar
func (m Model) renderStatusBar() string {
	left := fmt.Sprintf(" API: %s ", m.apiClient.BaseURL())
	right := fmt.Sprintf(" %s | ? help | q quit ", tabNames[m.activeTab])

	leftWidth := lipgloss.Width(left)
	rightWidth := lipgloss.Width(right)
	fillWidth := m.width - leftWidth - rightWidth
	if fillWidth < 0 {
		fillWidth = 0
	}

	return styleStatusBar.Render(left+strings.Repeat(" ", fillWidth)) + styleStatusBar.Render(right)
}

// renderHelp renders the help overlay
func (m Model) renderHelp() string {
	helpStyle := lipgloss.NewStyle().
		Background(colorBg).
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorBlue).
		Padding(1, 2)

	var b strings.Builder
	b.WriteString(styleHeader.Render("Keyboard Shortcuts"))
	b.WriteString("\n\n")

	b.WriteString(styleInfo.Render("Navigation") + "\n")
	b.WriteString(fmt.Sprintf("  %-20s Switch to Browse tab\n", "F1"))
	b.WriteString(fmt.Sprintf("  %-20s Switch to Search tab\n", "F2"))
	b.WriteString(fmt.Sprintf("  %-20s Switch to Tree View tab\n", "F3"))
	b.WriteString(fmt.Sprintf("  %-20s Switch to Registry tab\n", "F4"))
	b.WriteString(fmt.Sprintf("  %-20s Next tab\n", "Tab, →, l"))
	b.WriteString(fmt.Sprintf("  %-20s Previous tab\n", "Shift+Tab, ←, h"))
	b.WriteString("\n")

	b.WriteString(styleInfo.Render("Browse Tab") + "\n")
	b.WriteString(fmt.Sprintf("  %-20s Move up\n", "↑, k"))
	b.WriteString(fmt.Sprintf("  %-20s Move down\n", "↓, j"))
	b.WriteString(fmt.Sprintf("  %-20s View skill detail\n", "Enter"))
	b.WriteString(fmt.Sprintf("  %-20s Filter list\n", "/"))
	b.WriteString("\n")

	b.WriteString(styleInfo.Render("Search Tab") + "\n")
	b.WriteString(fmt.Sprintf("  %-20s Focus search input\n", "Tab, i"))
	b.WriteString(fmt.Sprintf("  %-20s Execute search\n", "Enter"))
	b.WriteString(fmt.Sprintf("  %-20s Clear results\n", "Ctrl+c"))
	b.WriteString("\n")

	b.WriteString(styleInfo.Render("Tree Tab") + "\n")
	b.WriteString(fmt.Sprintf("  %-20s Toggle expand/collapse\n", "Enter, Space"))
	b.WriteString(fmt.Sprintf("  %-20s Focus parent\n", "↑, k, h"))
	b.WriteString(fmt.Sprintf("  %-20s Focus next child\n", "↓, j, l"))
	b.WriteString("\n")

	b.WriteString(styleInfo.Render("General") + "\n")
	b.WriteString(fmt.Sprintf("  %-20s Reload data\n", "Ctrl+r, r"))
	b.WriteString(fmt.Sprintf("  %-20s Toggle this help\n", "?"))
	b.WriteString(fmt.Sprintf("  %-20s Quit application\n", "q, Ctrl+c"))

	return helpStyle.Render(b.String())
}

// overlayHelp overlays the help panel on top of the current view
func (m Model) overlayHelp(background, help string) string {
	helpWidth := lipgloss.Width(help)
	helpHeight := lipgloss.Height(help)

	// Center the help panel
	x := (m.width - helpWidth) / 2
	if x < 0 {
		x = 0
	}
	y := (m.height - helpHeight) / 2
	if y < 0 {
		y = 0
	}

	bgLines := strings.Split(background, "\n")
	helpLines := strings.Split(help, "\n")

	var result strings.Builder
	for i, line := range bgLines {
		if i >= y && i < y+helpHeight {
			// This line has help content overlay
			helpLineIdx := i - y
			if helpLineIdx < len(helpLines) {
				helpLine := helpLines[helpLineIdx]
				// Replace portion of background with help
				if x < len(line) {
					before := ""
					if x > 0 {
						before = line[:x]
					}
					after := ""
					afterStart := x + lipgloss.Width(helpLine)
					if afterStart < len(line) {
						after = line[afterStart:]
					}
					result.WriteString(before + helpLine + after + "\n")
				} else {
					result.WriteString(line + "\n")
				}
			} else {
				result.WriteString(line + "\n")
			}
		} else {
			result.WriteString(line + "\n")
		}
	}

	return result.String()
}

// contentHeight returns the available height for content
func (m Model) contentHeight() int {
	titleLines := 2  // title bar + tabs
	statusLines := 1 // status bar
	messageLines := 0
	if m.showMessage != "" {
		messageLines = 1
	}
	padding := 1
	return m.height - titleLines - statusLines - messageLines - padding
}

// contentWidth returns the available width for content
func (m Model) contentWidth() int {
	return m.width
}

// onTabChange handles tab switch events
func (m Model) onTabChange() tea.Cmd {
	// Each tab model can refresh when activated
	switch m.activeTab {
	case tabBrowse:
		return m.browseModel.OnActivate()
	case tabSearch:
		return m.searchModel.OnActivate()
	case tabTree:
		return m.treeModel.OnActivate()
	case tabRegistry:
		return m.registryModel.OnActivate()
	}
	return nil
}

// onReload handles data reload
func (m Model) onReload() tea.Cmd {
	return tea.Batch(
		m.browseModel.Reload(),
		m.searchModel.Reload(),
		m.treeModel.Reload(),
		m.registryModel.Reload(),
	)
}

// showErrorMessage displays a temporary error message
func (m *Model) showErrorMessage(msg string) {
	m.showMessage = msg
	m.msgType = "error"
}

// showSuccessMessage displays a temporary success message
func (m *Model) showSuccessMessage(msg string) {
	m.showMessage = msg
	m.msgType = "success"
}
