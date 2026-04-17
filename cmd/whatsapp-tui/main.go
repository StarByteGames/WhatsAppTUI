package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/StarGames2025/Logger"

	"DevStarByte/internal/client"
	"DevStarByte/internal/db"
	"DevStarByte/internal/state"
	"DevStarByte/internal/tui"
)

func main() {
	logger, _ := Logger.NewLogger(Logger.DEBUG, "./.log", false)
	logger.Info("Starting WhatsApp TUI...")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	// Initialise SQLite-backed device store.
	logger.Info("Initialising device store...")
	dbLog := waLog.Stdout("Database", "ERROR", true)
	container, err := sqlstore.New(ctx, "sqlite3", "file:whatsapp.db?_foreign_keys=on", dbLog)
	if err != nil {
		logger.Error("DB init failed: " + err.Error())
		os.Exit(10) // DB_INIT_ERROR
	}

	// Initialise message database.
	store, err := db.NewStore(logger)
	if err != nil {
		logger.Warning("Message DB init failed: " + err.Error())
	}

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		logger.Error("Device store error: " + err.Error())
		os.Exit(11) // DEVICE_STORE_ERROR
	}

	// Create whatsmeow client.
	logger.Info("Creating WhatsApp client...")
	clientLog := waLog.Stdout("Client", "ERROR", true)
	waClient := whatsmeow.NewClient(deviceStore, clientLog)

	// Create shared application state.
	appState := state.New(waClient, store, logger)

	waClient.AddEventHandler(client.NewEventHandler(appState))

	// Connect – pair via QR code if not yet registered.
	if waClient.Store.ID == nil {
		logger.Info("No existing session, starting QR code pairing...")
		qrCh, _ := waClient.GetQRChannel(ctx)
		if err = waClient.Connect(); err != nil {
			logger.Error("Connect failed: " + err.Error())
			os.Exit(appState.ExitCodes["ERROR"])
		}
		fmt.Println("\nScan the QR code below with WhatsApp on your phone:\n")
		for evt := range qrCh {
			switch evt.Event {
			case "code":
				client.DisplayQR(logger, evt.Code)
			case "success":
				logger.Info("QR code login successful")
				fmt.Println("\n✓ Logged in successfully!")
			case "timeout", "error":
				logger.Error("QR login failed: " + evt.Event)
				os.Exit(appState.ExitCodes["ERROR"])
			}
		}
	} else {
		logger.Info("Existing session found, reconnecting...")
		if err = waClient.Connect(); err != nil {
			logger.Error("Connect failed: " + err.Error())
			os.Exit(appState.ExitCodes["ERROR"])
		}
	}

	// Let the connection settle before loading chats.
	logger.Debug("Waiting for connection to settle...")
	time.Sleep(2 * time.Second)

	chats, err := client.LoadChats(appState, ctx)
	if err != nil {
		logger.Warning("Partial chat load: " + err.Error())
	}
	logger.Info(fmt.Sprintf("Loaded %d chats", len(chats)))

	// Pre-load all persisted messages into memory so they're available immediately.
	if allMsgs := store.LoadAllMessages(); allMsgs != nil {
		appState.MessagesMu.Lock()
		for chatJID, msgs := range allMsgs {
			for _, m := range msgs {
				found := false
				for _, existing := range appState.MessagesMap[chatJID] {
					if existing.ID == m.ID {
						found = true
						break
					}
				}
				if !found {
					appState.MessagesMap[chatJID] = append(appState.MessagesMap[chatJID], m)
				}
			}
		}
		appState.MessagesMu.Unlock()
	}

	// Start the bubbletea TUI.
	logger.Info("Starting TUI...")
	model := tui.NewModel(appState, chats)
	prog := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())

	go func() {
		select {
		case <-sigCh:
			prog.Quit()
		case <-ctx.Done():
		}
	}()

	if _, err = prog.Run(); err != nil {
		logger.Error("TUI error: " + err.Error())
		os.Exit(appState.ExitCodes["ERROR"])
	}

	waClient.Disconnect()
	logger.Info("WhatsApp client disconnected")
	if store != nil {
		store.Close()
		logger.Info("Message database closed")
	}
	logger.Info("WhatsApp TUI shutdown complete")
	os.Exit(0)
}
