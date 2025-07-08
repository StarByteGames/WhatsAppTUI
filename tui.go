package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle             = lipgloss.NewStyle()
	itemStyle              = lipgloss.NewStyle()
	selectedStyle          = lipgloss.NewStyle()
	helpStyle              = lipgloss.NewStyle()
	borderStyle            = lipgloss.NewStyle()
	chatListBorderStyle    = lipgloss.NewStyle()
	chatHistoryBorderStyle lipgloss.Style

	helpText = "\n↑/↓ to navigate • q to quit"
)

type tuiModel struct {
	cursor   int
	items    []string
	width    int
	height   int
	messages []string
}

func UpdateLipglossStyles(m tuiModel) tuiModel {
	borderStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63")).
		Padding(0, 1).
		Width(m.width - 2).
		Height(m.height - 2)

	chatListWidth := m.width / 3
	chatHistoryWidth := m.width - chatListWidth - 9

	itemStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		Width(chatListWidth - 4)

	selectedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("229")).
		Background(lipgloss.Color("57")).
		Bold(true).
		Width(chatListWidth - 4)

	chatListBorderStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63")).
		Height(m.height-8).
		Width(chatListWidth).
		Margin(0, 1, 0, 0)

	chatHistoryBorderStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63")).
		Height(m.height - 8).
		Width(chatHistoryWidth)

	titleStyle = lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("205")).
		Padding(0, 2).
		Align(lipgloss.Center).
		Width(m.width - 6)

	helpStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Italic(true).
		MarginTop(1)

	return m
}

func initialModel() tuiModel {
	var entries []string
	for _, chat := range chatlist {
		entries = append(entries, chat.Name)
	}

	var initialMessages []string
	if len(chatlist) > 0 {
		initialMessages = chatBuffers[chatlist[0].JID]
	}

	return tuiModel{
		cursor:   0,
		items:    entries,
		messages: initialMessages,
		width:    0,
		height:   0,
	}
}

func (m tuiModel) Init() tea.Cmd {
	return tea.EnterAltScreen
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				m.messages = chatBuffers[chatlist[m.cursor].JID]
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
				m.messages = chatBuffers[chatlist[m.cursor].JID]
			}
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m = UpdateLipglossStyles(m)

	}
	return m, nil
}

func (m tuiModel) View() string {
	maxLines := m.height - 11
	if maxLines < 1 {
		maxLines = 1
	}
	if maxLines > len(m.items) {
		maxLines = len(m.items)
	}

	var start int
	var cursorInView int

	half := maxLines / 2
	switch {
	case m.cursor <= half:
		start = 0
		cursorInView = m.cursor
	case m.cursor >= len(m.items)-half:
		start = len(m.items) - maxLines
		if start < 0 {
			start = 0
		}
		cursorInView = m.cursor - start
	default:
		start = m.cursor - half
		cursorInView = half
	}

	end := start + maxLines
	if end > len(m.items) {
		end = len(m.items)
	}

	displayItems := m.items[start:end]

	listContent := ""
	for i, item := range displayItems {
		if i == cursorInView {
			line := "> " + item
			if i == cursorInView {
				listContent += selectedStyle.Render(line) + "\n"
			} else {
				listContent += itemStyle.Render(line) + "\n"
			}

		} else {
			listContent += itemStyle.Render("  "+item) + "\n"
		}
	}

	listContent += helpStyle.Render(helpText)

	chatListRendered := chatListBorderStyle.Render(listContent)

	historyList := ""
	for _, msg := range m.messages {
		historyList += itemStyle.Render("  " + msg + "\n")
	}

	chatHistoryRendered := chatHistoryBorderStyle.Render(historyList)

	sideBySide := lipgloss.JoinHorizontal(lipgloss.Top, chatListRendered, chatHistoryRendered)

	return borderStyle.Render(titleStyle.Render("WhatsApp Contacts & Groups") + "\n\n" + sideBySide)
}
