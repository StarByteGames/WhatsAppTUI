package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"github.com/skip2/go-qrcode"

	"image"
	"image/png"
	"log"

	"github.com/blacktop/go-termimg"
	"github.com/nfnt/resize"
)

func DisplayImage(filePath string, width int, height int) string {
	file, err := os.Open(filePath)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		log.Fatal(err)
	}

	resizedImg := resize.Resize(uint(width), uint(height), img, resize.Lanczos3)

	tmpFile, err := os.Create("temp_resized.jpg")
	if err != nil {
		log.Fatal(err)
	}
	defer tmpFile.Close()

	err = png.Encode(tmpFile, resizedImg)
	if err != nil {
		log.Fatal(err)
	}

	ti, err := termimg.Open("temp_resized.jpg")
	if err != nil {
		log.Fatal(err)
	}
	imgStr, err := ti.Render()
	if err != nil {
		log.Fatal(err)
	}

	return imgStr
}

func GetImmage(imageString string, width int, height int) string {
	err := qrcode.WriteFile(imageString, qrcode.Medium, 256, "qrcode.png")
	if err != nil {
		log.Fatal(err)
	}

	file, err := os.Open("qrcode.png")
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		log.Fatal(err)
	}

	resizedImg := resize.Resize(uint(width), uint(height), img, resize.Lanczos3)

	tmpFile, err := os.Create("temp_resized.png")
	if err != nil {
		log.Fatal(err)
	}
	defer tmpFile.Close()

	err = png.Encode(tmpFile, resizedImg)
	if err != nil {
		log.Fatal(err)
	}

	ti, err := termimg.Open("temp_resized.png")
	if err != nil {
		log.Fatal(err)
	}
	imgStr, err := ti.Render()
	if err != nil {
		log.Fatal(err)
	}

	return imgStr
}

func downloadImage(url string, filename string) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("Fehler beim Herunterladen des Bildes: %v", err)
	}
	defer resp.Body.Close()

	out, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("Fehler beim Erstellen der Datei: %v", err)
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return fmt.Errorf("Fehler beim Speichern des Bildes: %v", err)
	}

	fmt.Println("Bild erfolgreich gespeichert:", filename)
	return nil
}

func eventHandler(evt interface{}) {
	switch v := evt.(type) {
	case *events.Message:
		sender := v.Info.PushName
		message := v.Message.GetConversation()

		if message == "" {
			downloadImage(v.Message.GetImageMessage().GetURL(), "received_image.jpg")
			message = DisplayImage("received_image.jpg", 100, 100)
		}

		fmt.Printf("Received a message from %s: %s\n\r", sender, message)
	}
}

func main() {
	dbLog := waLog.Stdout("Database", "", true)
	container, err := sqlstore.New("sqlite3", "file:sqlstore.db?_foreign_keys=on", dbLog)
	if err != nil {
		panic(err)
	}

	deviceStore, err := container.GetFirstDevice()
	if err != nil {
		panic(err)
	}
	clientLog := waLog.Stdout("Client", "", true)
	client := whatsmeow.NewClient(deviceStore, clientLog)
	client.AddEventHandler(eventHandler)

	if client.Store.ID == nil {
		qrChan, _ := client.GetQRChannel(context.Background())
		err = client.Connect()
		if err != nil {
			panic(err)
		}
		for evt := range qrChan {
			if evt.Event == "code" {
				fmt.Println("QR code:", GetImmage(evt.Code, 300, 300))
			} else {
				fmt.Println("Login event:", evt.Event)
			}
		}
	} else {
		err = client.Connect()
		if err != nil {
			panic(err)
		}
	}

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c

	client.Disconnect()
}
