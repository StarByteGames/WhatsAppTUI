package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"syscall"

	_ "github.com/mattn/go-sqlite3"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/skip2/go-qrcode"

	"image"
	"image/png"

	"github.com/blacktop/go-termimg"
	"github.com/nfnt/resize"

	"github.com/StarGames2025/Logger"
)

var (
	exitCodes = map[string]int{
		"ERROR":                -1,
		"SUCCESS":              0,
		"SHUTDOWN":             0,
		"DB_INIT_ERROR":        10,
		"DEVICE_STORE_ERROR":   11,
		"LOGGER_ERROR":         12,
		"QR_GENERATE_ERROR":    13,
		"QR_OPEN_ERROR":        14,
		"QR_DECODE_ERROR":      15,
		"QR_RESIZE_ERROR":      16,
		"QR_FILE_CREATE_ERROR": 17,
		"QR_FILE_ENCODE_ERROR": 18,
		"QR_RENDER_ERROR":      19,
		"GROUP_FETCH_ERROR":    20,
		"CONTACT_FETCH_ERROR":  21,
		"DATA_MARSHAL_ERROR":   22,
		"DATA_UNMARSHAL_ERROR": 23,
	}

	logger, _ = Logger.NewLogger(Logger.DEBUG, "./.log", false)

	grupsList    []*types.GroupInfo
	contactsList []contactStruct
	chatlist     []struct {
		JID  types.JID
		Name string
	}

	client      *whatsmeow.Client
	chatBuffers = make(map[types.JID][]string)
)

type (
	contactStruct struct {
		JID     types.JID
		Contact types.ContactInfo
	}
)

func GetQRCodeImage(imageString string, width int, height int) string {
	logger.Info("Generating QR Code...")
	err := qrcode.WriteFile(imageString, qrcode.Medium, 256, "qrcode.png")
	if err != nil {
		logger.Fatal("QR_GENERATE_ERROR", "Failed to generate QR Code:", err.Error())
	}

	file, err := os.Open("qrcode.png")
	if err != nil {
		logger.Fatal("QR_OPEN_ERROR", "Failed to open QR Code file:", err.Error())
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		logger.Fatal("QR_DECODE_ERROR", "Failed to decode QR Code image:", err.Error())
	}

	logger.Debug("Resizing QR Code image...")
	resizedImg := resize.Resize(uint(width), uint(height), img, resize.Lanczos3)
	if resizedImg.Bounds().Dx() == 0 || resizedImg.Bounds().Dy() == 0 {
		logger.Fatal("QR_RESIZE_ERROR", "Resized QR Code image is invalid")
	}

	tmpFile, err := os.Create("temp_resized.png")
	if err != nil {
		logger.Error("Failed to create resized QR Code file:", err.Error())
	}
	defer tmpFile.Close()

	err = png.Encode(tmpFile, resizedImg)
	if err != nil {
		logger.Error("Failed to encode resized QR Code:", err.Error())
	}

	logger.Info("QR Code generated successfully.")

	ti, err := termimg.Open("temp_resized.png")
	if err != nil {
		logger.Error("Failed to render QR Code in terminal:", err.Error())
	}
	imgStr, err := ti.Render()
	if err != nil {
		logger.Error("Failed to get QR Code as string:", err.Error())
	}

	return imgStr
}

func eventHandler(evt interface{}) {
	switch v := evt.(type) {
	case *events.Message:
		sender := v.Info.PushName
		message := v.Message.GetConversation()
		logger.Info(fmt.Sprintf("Message from %s in chat %s: %s", sender, v.Info.Chat.String(), message))

		chatBuffers[v.Info.Chat] = append(chatBuffers[v.Info.Chat], fmt.Sprintf("%s: %s", sender, message))
	case *events.HistorySync:
		logger.Info(fmt.Sprintf("History sync with %d conversations", len(v.Data.Conversations)))
		for _, conv := range v.Data.Conversations {
			jid, err := types.ParseJID(*conv.ID)
			if err != nil {
				logger.Warning("Invalid JID in history sync: " + err.Error())
				continue
			}
			logger.Info("History for chat: " + jid.String())

			for _, wm := range conv.Messages {
				parsedMsg, err := client.ParseWebMessage(jid, wm.Message)
				if err != nil {
					logger.Warning("Failed to parse web message: " + err.Error())
					continue
				}
				if parsedMsg.Message.GetConversation() != "" {
					timestamp := parsedMsg.Info.Timestamp.Format("15:04")
					sender := parsedMsg.Info.PushName
					chatBuffers[jid] = append(chatBuffers[jid], fmt.Sprintf("[%s] %s: %s", timestamp, sender, parsedMsg.Message.GetConversation()))
				}
			}
		}
	}
}

func main() {
	logger.Info("Starting WhatsApp Client...")

	dbLog := waLog.Stdout("Database", "ERROR", true)
	container, err := sqlstore.New(context.Background(), "sqlite3", "file:sqlstore.db?_foreign_keys=on", dbLog)
	if err != nil {
		logger.Fatal("DB_INIT_ERROR", "Failed to initialize database:", err.Error())
	}

	deviceStore, err := container.GetFirstDevice(context.Background())
	if err != nil {
		logger.Fatal("DEVICE_STORE_ERROR", "Failed to get device store:", err.Error())
	}

	clientLog := waLog.Stdout("Client", "ERROR", true)
	client = whatsmeow.NewClient(deviceStore, clientLog)
	client.AddEventHandler(eventHandler)

	if client.Store.ID == nil {
		logger.Info("No session found, generating QR Code...")
		qrChan, _ := client.GetQRChannel(context.Background())
		err = client.Connect()
		if err != nil {
			logger.Fatal("CLIENT_CONNECT_ERROR", "Failed to connect client:", err.Error())
		}

		for evt := range qrChan {
			if evt.Event == "code" {
				logger.Info("Displaying QR Code. Scan with WhatsApp.")
				fmt.Println("QR code:", GetQRCodeImage(evt.Code, 300, 300))
			} else {
				logger.Info("Login event received:", evt.Event)
			}
		}
	} else {
		logger.Info("Session found. Connecting...")
		err = client.Connect()
		if err != nil {
			logger.Fatal("CLIENT_CONNECT_ERROR", "Failed to connect client:", err.Error())
		}

		logger.Info("Fetching joined groups...")
		groups, err := client.GetJoinedGroups()
		if err != nil {
			logger.Fatal("GROUP_FETCH_ERROR", "Failed to fetch groups:", err.Error())
		} else {
			logger.Info(fmt.Sprintf("Found %d groups.", len(groups)))
		}

		for _, group := range groups {
			logger.Debug(fmt.Sprintf("Group: %s - %s", group.JID, group.Name))
			grupsList = append(grupsList, group)
			chatlist = append(chatlist, struct {
				JID  types.JID
				Name string
			}{
				JID:  group.JID,
				Name: group.Name,
			})
		}

		logger.Info("Fetching contacts...")
		contacts, err := client.Store.Contacts.GetAllContacts(context.Background())
		if err != nil {
			logger.Fatal("CONTACT_FETCH_ERROR", "Failed to fetch contacts:", err.Error())
		} else {
			logger.Info(fmt.Sprintf("Found %d contacts.", len(contacts)))
		}

		for jid, contact := range contacts {
			name := contact.FullName
			if name == "" {
				name = contact.PushName
			}
			logger.Debug(fmt.Sprintf("%-20s - %s", jid, name))
			contactsList = append(contactsList, struct {
				JID     types.JID
				Contact types.ContactInfo
			}{
				JID:     jid,
				Contact: contact,
			})
			chatlist = append(chatlist, struct {
				JID  types.JID
				Name string
			}{
				JID:  jid,
				Name: name,
			})
		}

		sort.Slice(chatlist, func(i, j int) bool {
			return chatlist[i].Name < chatlist[j].Name
		})

		logger.Info("WhatsApp Client started successfully.")
	}

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		p := tea.NewProgram(initialModel())
		if err := p.Start(); err != nil {
			logger.Fatal("TUI_START_ERROR", "Failed to start TUI:", err.Error())
		}
	}()
	<-c

	fmt.Print("\033[100D")
	fmt.Print("\033[2K")

	logger.Info("Shutting down...")
	client.Disconnect()
	logger.Info("Exiting.")
	os.Exit(exitCodes["SHUTDOWN"])
}

func init() {
	logger.ExitCodes = exitCodes
}

// --------------------------------------------------------------------------------------------------------------------------------- TUI ---------------------------------------------------------------------------------------------

var (
	titleStyle          = lipgloss.NewStyle()
	itemStyle           = lipgloss.NewStyle()
	selectedStyle       = lipgloss.NewStyle()
	helpStyle           = lipgloss.NewStyle()
	borderStyle         = lipgloss.NewStyle()
	chatListBorderStyle = lipgloss.NewStyle()

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
	const (
		padVertical   = 0
		padHorizontal = 1
		titleHeight   = 1
		titleMargin   = 1
	)

	borderStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63")).
		Padding(padVertical, padHorizontal).
		Width(m.width - (2 * padHorizontal)).
		Height(m.height - (2*padVertical + titleHeight + 4 + titleMargin + 2))

	itemStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("241"))

	selectedStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("229")).
		Background(lipgloss.Color("57")).
		Bold(true).
		Width(m.width/3 - (2*padHorizontal + 2)).
		MaxWidth(m.width/3 - (2*padHorizontal + 2))

	titleStyle = lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("205")).
		Padding(0, 2).
		Align(lipgloss.Center).
		Width(m.width - (2*padHorizontal + 4))

	helpStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Italic(true).
		MarginTop(1)

	chatListBorderStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63")).
		Height(m.height - (2*padVertical + titleHeight + 4 + titleMargin + 2)).
		Width(m.width - (m.width/2 + (2 * padHorizontal) + 2))

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
			listContent += selectedStyle.Render("> "+item) + "\n"
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
	chatHistoryRendered := chatListBorderStyle.Render(historyList)

	sideBySide := lipgloss.JoinHorizontal(lipgloss.Top, chatListRendered, chatHistoryRendered)

	return borderStyle.Render(titleStyle.Render("WhatsApp Contacts & Groups") + "\n\n" + sideBySide)
}
