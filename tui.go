package main

import (
	"bufio"
	"encoding/json"
	"log"
	"net"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"go.mau.fi/whatsmeow/types"
)

// Main Model
type model struct {
	contacts []contactStruct
	groups   []*types.GroupInfo
	ready    bool
}

// Initial Model Sezup
func initialModel() model {
	contacts, groups := initGetData()
	return model{
		contacts: contacts,
		groups:   groups,
		ready:    false,
	}
}

func (m model) Init() tea.Cmd {
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit
		}
	}

	return m, tea.Batch(cmds...)
}

func (m model) View() string {
	if !m.ready {
		return "\nInitializing...\n"
	}

	return ""
}

func initGetData() ([]contactStruct, []*types.GroupInfo) {
	var (
		contacts  []contactStruct
		grupsList []*types.GroupInfo
	)

	logger.Info("Fetching contacts and groups...")
	scanner := bufio.NewScanner(connection)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "SetContact\\\\") {
			logger.Debug("Contact:", scanner.Text())
			var contact contactStruct
			err := json.Unmarshal([]byte(strings.Trim(scanner.Text(), "SetContact\\\\")), &contact)
			if err != nil {
				logger.Fatal("DATA_UNMARSHAL_ERROR", "Failed to unmashal Conteact:", err.Error())
			}
			contacts = append(contacts, contact)
		} else if strings.Contains(scanner.Text(), "SetGroup\\\\") {
			logger.Debug("Group:", scanner.Text())
			var group *types.GroupInfo
			err := json.Unmarshal([]byte(strings.Trim(scanner.Text(), "SetGroup\\\\")), &group)
			if err != nil {
				logger.Fatal("DATA_UNMARSHAL_ERROR", "Failed to unmashal Group:", err.Error())
			}
			grupsList = append(grupsList, group)
		} else if scanner.Text() == "END\\\\" {
			break
		}
	}
	return contacts, grupsList
}

func StartClient() net.Conn {
	conn, err := net.Dial("tcp", "localhost:8080")
	if err != nil {
		log.Fatal("Failed to connect to server:", err)
	}

	return conn
}

func main() {
	connection = StartClient()

	initial := initialModel()

	p := tea.NewProgram(initial)

	if err := p.Start(); err != nil {
		logger.Fatal("CLIENT_RUN_ERROR", "Failed to launch tui", err.Error())
	}
}
