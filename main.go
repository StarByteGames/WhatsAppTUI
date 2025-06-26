package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbletea"
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
		"ERROR":                       -1,
		"SUCCESS":                     0,
		"SHUTDOWN":                    0,
		"DB_INIT_ERROR":               10,
		"DEVICE_STORE_ERROR":          11,
		"LOGGER_ERROR":      	       12,
		"QR_GENERATE_ERROR":           13,
		"QR_OPEN_ERROR":               14,
		"QR_DECODE_ERROR":             15,
		"QR_RESIZE_ERROR":             16,
		"QR_FILE_CREATE_ERROR":        17,
		"QR_FILE_ENCODE_ERROR":        18,
		"QR_RENDER_ERROR":             19,
		"GROUP_FETCH_ERROR":           20,
		"CONTACT_FETCH_ERROR":         21,
		"DATA_MARSHAL_ERROR":          22,
		"DATA_UNMARSHAL_ERROR":        23,
	}

	logger, _ = Logger.NewLogger(Logger.DEBUG, "./.log", false)

	grupsList    []*types.GroupInfo
	contactsList []contactStruct
	chatlist     []struct {
		JID  types.JID
		Name string
	}
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

		logger.Info(fmt.Sprintf("Received a message from %s: %s\n\r", sender, message))
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
	client := whatsmeow.NewClient(deviceStore, clientLog)
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
			logger.Debug(fmt.Sprintf("%-20s - %s", jid, contact.PushName))
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
				Name: contact.PushName,
			})
		}
	}

	time.Sleep(1 * time.Second)

	// Signal-Handling
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
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