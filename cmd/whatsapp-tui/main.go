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
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/StarGames2025/Logger"

	"DevStarByte/internal/client"
	"DevStarByte/internal/db"
	"DevStarByte/internal/state"
	"DevStarByte/internal/tui"
)

// Process exit codes.
const (
	exitError            = 1
	exitDBInitError      = 10
	exitDeviceStoreError = 11
)

func main() {
	os.Exit(run())
}

// fail logs an error and also prints it, since the log file is easy to miss.
func fail(logger *Logger.Logger, code int, msg string) int {
	logger.Error(msg)
	fmt.Fprintln(os.Stderr, "Error: "+msg)
	return code
}

func run() int {
	logger, err := Logger.NewLogger(Logger.DEBUG, "./.log", false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to create logger: "+err.Error())
		return exitError
	}
	logger.Info("Starting WhatsApp TUI...")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialise SQLite-backed device store.
	logger.Info("Initialising device store...")
	container, err := sqlstore.New(ctx, "sqlite3", "file:whatsapp.db?_foreign_keys=on", waLog.Stdout("Database", "ERROR", true))
	if err != nil {
		return fail(logger, exitDBInitError, "DB init failed: "+err.Error())
	}

	// Message database is optional: without it messages are kept in memory only.
	msgStore, err := db.NewStore(logger)
	if err != nil {
		logger.Warning("Message DB init failed, messages will not be persisted: " + err.Error())
	}
	defer func() {
		msgStore.Close()
		logger.Info("Message database closed")
	}()

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		return fail(logger, exitDeviceStoreError, "Device store error: "+err.Error())
	}

	useLatestWAVersion(ctx, logger)

	logger.Info("Creating WhatsApp client...")
	waClient := whatsmeow.NewClient(deviceStore, waLog.Stdout("Client", "ERROR", true))
	// Also emit app state events (e.g. pins) for full syncs, not only for
	// changes, so the pin order is known right after pairing.
	waClient.EmitAppStateEventsOnFullSync = true
	appState := state.New(waClient, msgStore, logger)

	// Load persisted messages before connecting, so that live and history
	// events are merged into them instead of racing with the load.
	appState.LoadPersisted(msgStore.LoadAllMessages())
	waClient.AddEventHandler(client.NewEventHandler(appState))

	if err := connect(ctx, waClient, logger); err != nil {
		return fail(logger, exitError, err.Error())
	}
	defer func() {
		waClient.Disconnect()
		logger.Info("WhatsApp client disconnected")
	}()

	// Let the connection settle before loading chats.
	time.Sleep(2 * time.Second)
	if err := client.LoadChats(appState, ctx); err != nil {
		logger.Warning("Partial chat load: " + err.Error())
	}

	logger.Info("Starting TUI...")
	prog := tea.NewProgram(tui.NewModel(appState), tea.WithAltScreen(), tea.WithMouseCellMotion())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-sigCh:
			prog.Quit()
		case <-ctx.Done():
		}
	}()

	if _, err := prog.Run(); err != nil {
		return fail(logger, exitError, "TUI error: "+err.Error())
	}
	logger.Info("WhatsApp TUI shutdown complete")
	return 0
}

// useLatestWAVersion asks WhatsApp Web for its current version. WhatsApp
// rejects clients that report an outdated version (405), so this keeps pairing
// working across version bumps; on failure the built-in version is kept.
func useLatestWAVersion(ctx context.Context, logger *Logger.Logger) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	v, err := whatsmeow.GetLatestVersion(ctx, nil)
	if err != nil {
		logger.Warning("Could not fetch latest WhatsApp Web version, using built-in: " + err.Error())
		return
	}
	logger.Info("Using WhatsApp Web version " + v.String())
	store.SetWAVersion(*v)
}

// connect connects the client, pairing via QR code if not yet registered.
func connect(ctx context.Context, waClient *whatsmeow.Client, logger *Logger.Logger) error {
	if waClient.Store.ID != nil {
		logger.Info("Existing session found, reconnecting...")
		if err := waClient.Connect(); err != nil {
			return fmt.Errorf("connect failed: %w", err)
		}
		return nil
	}

	logger.Info("No existing session, starting QR code pairing...")
	qrCh, err := waClient.GetQRChannel(ctx)
	if err != nil {
		return fmt.Errorf("QR channel failed: %w", err)
	}
	if err := waClient.Connect(); err != nil {
		return fmt.Errorf("connect failed: %w", err)
	}
	fmt.Print("\nScan the QR code below with WhatsApp on your phone:\n\n")
	for evt := range qrCh {
		switch evt.Event {
		case "code":
			client.DisplayQR(logger, evt.Code)
		case "success":
			logger.Info("QR code login successful")
			fmt.Println("\n✓ Logged in successfully!")
			return nil
		case whatsmeow.QRChannelClientOutdated.Event:
			waClient.Disconnect()
			return fmt.Errorf("WhatsApp rejected this client as outdated (405): update go.mau.fi/whatsmeow and rebuild")
		case whatsmeow.QRChannelTimeout.Event:
			waClient.Disconnect()
			return fmt.Errorf("QR login failed: timed out or the connection was closed (check the log in ./.log)")
		default:
			// error or an unexpected event: pairing cannot continue.
			waClient.Disconnect()
			if evt.Error != nil {
				return fmt.Errorf("QR login failed: %s: %w", evt.Event, evt.Error)
			}
			return fmt.Errorf("QR login failed: %s", evt.Event)
		}
	}
	return nil
}
