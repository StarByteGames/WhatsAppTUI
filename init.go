package main

import (
	"net"

	"github.com/StarGames2025/Logger"
	"go.mau.fi/whatsmeow/types"
)

var (
	exitCodes = map[string]int{
		"ERROR":                       -1,
		"SUCCESS":                     0,
		"SHUTDOWN":                    0,
		"DB_INIT_ERROR":               10,
		"DEVICE_STORE_ERROR":          11,
		"CLIENT_CONNECT_ERROR":        12,
		"QR_GENERATE_ERROR":           13,
		"QR_OPEN_ERROR":               14,
		"QR_DECODE_ERROR":             15,
		"QR_RESIZE_ERROR":             16,
		"QR_FILE_CREATE_ERROR":        17,
		"QR_FILE_ENCODE_ERROR":        18,
		"QR_RENDER_ERROR":             19,
		"GROUP_FETCH_ERROR":           20,
		"CONTACT_FETCH_ERROR":         21,
		"SERVER_START_ERROR":          22,
		"SERVER_ACCEPT_ERROR":         23,
		"CLIENT_CONNECT_SERVER_ERROR": 24,
		"CLIENT_RUN_ERROR":            25,
		"DATA_MARSHAL_ERROR":          26,
		"DATA_UNMARSHAL_ERROR":        27,
	}

	logger = Logger.NewLogger(Logger.DEBUG)

	connection   net.Conn
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

func init() {
	logger.ExitCodes = exitCodes
}
