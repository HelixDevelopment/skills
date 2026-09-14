package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	defaultAPIURL = "http://localhost:8080"
	appVersion    = "1.0.0"
)

var (
	apiURL  = flag.String("api-url", defaultAPIURL, "Base URL of the skill system API")
	apiKey  = flag.String("api-key", "", "API key for authentication")
	version = flag.Bool("version", false, "Print version and exit")
)

func main() {
	flag.Parse()

	if *version {
		fmt.Printf("helix-skills-tui v%s\n", appVersion)
		os.Exit(0)
	}

	// Initialize API client
	client := NewAPIClient(*apiURL, *apiKey)

	// Test connectivity with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	connected := client.HealthCheck(ctx)
	cancel()

	// Create initial model
	initialModel := NewModel(client, connected)

	// Run Bubble Tea program
	p := tea.NewProgram(
		initialModel,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running TUI: %v\n", err)
		os.Exit(1)
	}
}

// Global lipgloss styles - low saturation professional palette
var (
	// Base colors
	colorBg      = lipgloss.Color("#1e1e2e")
	colorFg      = lipgloss.Color("#cdd6f4")
	colorSurface = lipgloss.Color("#313244")
	colorOverlay = lipgloss.Color("#45475a")

	// Accent colors (low saturation)
	colorBlue   = lipgloss.Color("#89b4fa")
	colorGreen  = lipgloss.Color("#a6e3a1")
	colorYellow = lipgloss.Color("#f9e2af")
	colorRed    = lipgloss.Color("#f38ba8")
	colorPurple = lipgloss.Color("#cba6f7")
	colorTeal   = lipgloss.Color("#94e2d5")
	colorOrange = lipgloss.Color("#fab387")
	colorGray   = lipgloss.Color("#6c7086")

	// Semantic styles
	styleTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorBlue).
			MarginLeft(1)

	styleTab = lipgloss.NewStyle().
			Padding(0, 2).
			Foreground(colorGray)

	styleTabActive = lipgloss.NewStyle().
			Padding(0, 2).
			Bold(true).
			Foreground(colorBlue).
			Background(colorSurface).
			Border(lipgloss.RoundedBorder(), false, false, true, false).
			BorderForeground(colorBlue)

	styleError = lipgloss.NewStyle().
			Foreground(colorRed).
			Bold(true)

	styleSuccess = lipgloss.NewStyle().
			Foreground(colorGreen)

	styleWarning = lipgloss.NewStyle().
			Foreground(colorYellow)

	styleInfo = lipgloss.NewStyle().
			Foreground(colorTeal)

	styleHelp = lipgloss.NewStyle().
			Foreground(colorGray).
			Italic(true)

	styleStatusBar = lipgloss.NewStyle().
			Background(colorSurface).
			Foreground(colorGray).
			Padding(0, 1)

	styleHeader = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorPurple)

	styleSelected = lipgloss.NewStyle().
			Background(colorSurface).
			Foreground(colorBlue).
			Bold(true)

	styleNormal = lipgloss.NewStyle().
			Foreground(colorFg)

	styleBorder = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(colorOverlay)
)
