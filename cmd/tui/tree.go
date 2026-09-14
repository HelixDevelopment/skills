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

// TreeModel shows a skill dependency tree with expandable/collapsible nodes
type TreeModel struct {
	client     *APIClient
	root       *TreeNode
	focused    *TreeNode
	expanded   map[string]bool
	rootSkill  string
	input      textinput.Model
	height     int
	width      int
	loading    bool
	err        error
	focusInput bool
	flatNodes  []*TreeNode // flattened for navigation
	focusIdx   int
}

// TreeNode represents a node in the skill tree
type TreeNode struct {
	Skill    models.Skill
	Children []*TreeNode
	Depth    int
	Parent   *TreeNode
	Relation string // "requires", "extends", "recommends", "root"
}

// NewTreeModel creates a new tree model
func NewTreeModel(client *APIClient) TreeModel {
	ti := textinput.New()
	ti.Placeholder = "Enter skill name and press Enter..."
	ti.CharLimit = 100
	ti.Width = 40
	ti.PromptStyle = lipgloss.NewStyle().Foreground(colorBlue)
	ti.TextStyle = lipgloss.NewStyle().Foreground(colorFg)
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(colorBlue)
	ti.PlaceholderStyle = lipgloss.NewStyle().Foreground(colorOverlay)

	return TreeModel{
		client:     client,
		expanded:   make(map[string]bool),
		input:      ti,
		focusInput: true,
		rootSkill:  "",
	}
}

// Init implements tea.Model
func (m TreeModel) Init() tea.Cmd {
	if m.focusInput {
		return textinput.Blink
	}
	return nil
}

// treeMsg types
type treeLoadedMsg struct {
	root *TreeNode
}

type treeErrMsg struct {
	err error
}

// Update implements tea.Model
func (m TreeModel) Update(msg tea.Msg) (TreeModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.input.Width = msg.Width - 6
		return m, nil

	case treeLoadedMsg:
		m.loading = false
		m.root = msg.root
		m.err = nil
		m.focused = msg.root
		m.focusInput = false
		m.input.Blur()
		// Expand root by default
		if m.root != nil {
			m.expanded[m.root.Skill.Name] = true
			m.rebuildFlatNodes()
		}
		return m, nil

	case treeErrMsg:
		m.loading = false
		m.err = msg.err
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			if m.focusInput {
				skillName := m.input.Value()
				if skillName != "" {
					m.rootSkill = skillName
					m.loading = true
					m.root = nil
					m.focused = nil
					m.flatNodes = nil
					m.focusIdx = 0
					return m, m.loadTree(skillName)
				}
			} else if m.focused != nil {
				// Toggle expansion
				name := m.focused.Skill.Name
				if m.expanded[name] {
					delete(m.expanded, name)
				} else {
					m.expanded[name] = true
				}
				m.rebuildFlatNodes()
			}
			return m, nil

		case "tab", "i":
			if m.root != nil {
				m.focusInput = !m.focusInput
				if m.focusInput {
					m.input.Focus()
					return m, textinput.Blink
				}
				m.input.Blur()
			}
			return m, nil

		case "j", "down":
			if !m.focusInput && len(m.flatNodes) > 0 {
				if m.focusIdx < len(m.flatNodes)-1 {
					m.focusIdx++
					m.focused = m.flatNodes[m.focusIdx]
				}
			}
			return m, nil

		case "k", "up":
			if !m.focusInput && len(m.flatNodes) > 0 {
				if m.focusIdx > 0 {
					m.focusIdx--
					m.focused = m.flatNodes[m.focusIdx]
				}
			}
			return m, nil

		case "h", "left":
			if !m.focusInput && m.focused != nil {
				// Collapse current node or go to parent
				name := m.focused.Skill.Name
				if m.expanded[name] && len(m.focused.Children) > 0 {
					delete(m.expanded, name)
					m.rebuildFlatNodes()
				} else if m.focused.Parent != nil {
					// Find parent in flat nodes
					for i, n := range m.flatNodes {
						if n == m.focused.Parent {
							m.focusIdx = i
							m.focused = n
							break
						}
					}
				}
			}
			return m, nil

		case "l", "right":
			if !m.focusInput && m.focused != nil && len(m.focused.Children) > 0 {
				// Expand and focus first child
				m.expanded[m.focused.Skill.Name] = true
				m.rebuildFlatNodes()
				// Find first child
				for i, n := range m.flatNodes {
					if n.Parent == m.focused {
						m.focusIdx = i
						m.focused = n
						break
					}
				}
			}
			return m, nil

		case " ":
			// Toggle expansion with space
			if !m.focusInput && m.focused != nil {
				name := m.focused.Skill.Name
				if m.expanded[name] {
					delete(m.expanded, name)
				} else {
					m.expanded[name] = true
				}
				m.rebuildFlatNodes()
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
		return m, cmd
	}

	return m, nil
}

// View implements tea.Model
func (m TreeModel) View() string {
	var b strings.Builder

	b.WriteString(styleHeader.Render("Skill Dependency Tree"))
	b.WriteString("\n\n")

	// Input area
	inputStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		Padding(0, 1)
	if m.focusInput {
		inputStyle = inputStyle.BorderForeground(colorBlue)
	} else {
		inputStyle = inputStyle.BorderForeground(colorOverlay)
	}
	b.WriteString(inputStyle.Render(m.input.View()))

	if m.rootSkill != "" {
		b.WriteString(" ")
		b.WriteString(styleHelp.Render("(root: " + m.rootSkill + ")"))
	}
	b.WriteString("\n\n")

	if m.loading {
		b.WriteString(styleWarning.Render("Loading tree..."))
		return b.String()
	}

	if m.err != nil {
		b.WriteString(styleError.Render("Error: " + m.err.Error()))
		return b.String()
	}

	if m.root == nil {
		b.WriteString(styleHelp.Render("Enter a skill name above to view its dependency tree."))
		b.WriteString("\n\n")
		b.WriteString(styleHelp.Render("Navigation: ↑/↓ to move, Enter/Space to expand/collapse, ←/→ for parent/child"))
		return b.String()
	}

	// Render tree
	if len(m.flatNodes) > 0 {
		// Legend
		b.WriteString(m.renderLegend())
		b.WriteString("\n\n")

		// Visible range
		visibleCount := m.height - 8
		if visibleCount < 1 {
			visibleCount = 1
		}

		startIdx := 0
		if m.focusIdx >= visibleCount {
			startIdx = m.focusIdx - visibleCount + 1
		}

		for i := startIdx; i < len(m.flatNodes) && i < startIdx+visibleCount; i++ {
			b.WriteString(m.renderTreeNode(m.flatNodes[i], i == m.focusIdx))
			b.WriteString("\n")
		}

		// Detail panel for focused node
		if m.focused != nil && !m.focusInput {
			b.WriteString("\n")
			b.WriteString(m.renderDetail())
		}
	}

	return b.String()
}

// renderLegend shows the color legend
func (m TreeModel) renderLegend() string {
	var parts []string
	parts = append(parts, lipgloss.NewStyle().Foreground(colorRed).Render("● requires"))
	parts = append(parts, lipgloss.NewStyle().Foreground(colorBlue).Render("● extends"))
	parts = append(parts, lipgloss.NewStyle().Foreground(colorGreen).Render("● recommends"))
	return strings.Join(parts, "  ")
}

// renderTreeNode renders a single tree node
func (m TreeModel) renderTreeNode(node *TreeNode, focused bool) string {
	var b strings.Builder

	// Indentation
	indent := strings.Repeat("  ", node.Depth)

	// Branch connector
	connector := "├──"
	if node.Depth > 0 {
		// Check if last child
		if node.Parent != nil {
			isLast := false
			for i, child := range node.Parent.Children {
				if child == node {
					isLast = (i == len(node.Parent.Children)-1)
					break
				}
			}
			if isLast {
				connector = "└──"
			}
		}
	} else {
		connector = "◆"
	}

	// Relation color
	relationColor := colorGray
	switch node.Relation {
	case string(models.DepTypeRequires):
		relationColor = colorRed
	case string(models.DepTypeExtends):
		relationColor = colorBlue
	case string(models.DepTypeRecommends):
		relationColor = colorGreen
	case "root":
		relationColor = colorPurple
	}

	nameStyle := lipgloss.NewStyle().Foreground(colorFg)
	if focused {
		nameStyle = lipgloss.NewStyle().Foreground(colorBlue).Bold(true).Background(colorSurface)
	}

	// Expand/collapse indicator
	expandInd := ""
	if len(node.Children) > 0 {
		if m.expanded[node.Skill.Name] {
			expandInd = lipgloss.NewStyle().Foreground(colorGray).Render(" ▼")
		} else {
			expandInd = lipgloss.NewStyle().Foreground(colorGray).Render(" ▶")
		}
	}

	relationDot := lipgloss.NewStyle().Foreground(relationColor).Render("●")
	name := nameStyle.Render(node.Skill.Name)
	version := lipgloss.NewStyle().Foreground(colorGray).Render("v" + node.Skill.Version)

	b.WriteString(fmt.Sprintf("%s%s %s %s %s%s", indent, connector, relationDot, name, version, expandInd))

	return b.String()
}

// renderDetail shows details for the focused node
func (m TreeModel) renderDetail() string {
	if m.focused == nil {
		return ""
	}

	s := m.focused.Skill
	var b strings.Builder

	titleStyle := lipgloss.NewStyle().Foreground(colorBlue).Bold(true)
	b.WriteString(titleStyle.Render(s.Name))
	b.WriteString("\n")

	if s.Title != "" {
		b.WriteString(styleNormal.Render(s.Title))
		b.WriteString("\n")
	}
	if s.Description != "" {
		desc := s.Description
		if len(desc) > m.width-4 {
			desc = desc[:m.width-4] + "..."
		}
		b.WriteString(styleNormal.Render(desc))
		b.WriteString("\n")
	}

	if m.focused.Relation != "root" && m.focused.Relation != "" {
		relColor := colorGray
		switch m.focused.Relation {
		case string(models.DepTypeRequires):
			relColor = colorRed
		case string(models.DepTypeExtends):
			relColor = colorBlue
		case string(models.DepTypeRecommends):
			relColor = colorGreen
		}
		b.WriteString(lipgloss.NewStyle().Foreground(relColor).Render("Relation: " + m.focused.Relation))
	}

	return b.String()
}

// rebuildFlatNodes rebuilds the flattened node list based on expansion state
func (m *TreeModel) rebuildFlatNodes() {
	m.flatNodes = nil
	if m.root == nil {
		return
	}
	m.addVisibleNodes(m.root)
}

// addVisibleNodes recursively adds visible nodes to flat list
func (m *TreeModel) addVisibleNodes(node *TreeNode) {
	m.flatNodes = append(m.flatNodes, node)
	if m.expanded[node.Skill.Name] {
		for _, child := range node.Children {
			m.addVisibleNodes(child)
		}
	}
}

// loadTree fetches the skill tree from the API
func (m TreeModel) loadTree(name string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		node, err := m.client.GetTree(ctx, name, 5)
		if err != nil {
			return treeErrMsg{err: err}
		}

		// Convert API node to TreeNode
		root := convertAPINode(node, 0, nil, "root")
		return treeLoadedMsg{root: root}
	}
}

// convertAPINode converts a models.SkillTreeNode to our TreeNode
func convertAPINode(apiNode *models.SkillTreeNode, depth int, parent *TreeNode, relation string) *TreeNode {
	if apiNode == nil {
		return nil
	}

	node := &TreeNode{
		Skill:    apiNode.Skill,
		Depth:    depth,
		Parent:   parent,
		Relation: relation,
		Children: make([]*TreeNode, 0, len(apiNode.Children)),
	}

	for _, child := range apiNode.Children {
		// Determine relation from the child's dependencies
		childRelation := ""
		for _, dep := range child.Skill.Dependencies {
			if dep.DependsOn == apiNode.Skill.ID {
				childRelation = string(dep.RelationType)
				break
			}
		}
		if childRelation == "" {
			childRelation = "related"
		}

		childNode := convertAPINode(&child, depth+1, node, childRelation)
		if childNode != nil {
			node.Children = append(node.Children, childNode)
		}
	}

	return node
}

// SetSize updates the model dimensions
func (m TreeModel) SetSize(width, height int) tea.Cmd {
	m.width = width
	m.height = height
	m.input.Width = width - 6
	return nil
}

// OnActivate is called when this tab becomes active
func (m TreeModel) OnActivate() tea.Cmd {
	if m.focusInput {
		return textinput.Blink
	}
	return nil
}

// Reload forces a data refresh
func (m TreeModel) Reload() tea.Cmd {
	if m.rootSkill != "" {
		m.loading = true
		return m.loadTree(m.rootSkill)
	}
	return nil
}
