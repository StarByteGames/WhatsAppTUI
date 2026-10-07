# WhatsApp TUI 0.3.4

WhatsApp in your terminal — no browser, no Electron, no bloat.

![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white)
![License](https://img.shields.io/badge/License-MIT-green)
![Platform](https://img.shields.io/badge/Platform-Linux-FCC624?logo=linux&logoColor=black)
![Release](https://img.shields.io/badge/Release-Alpha-orange)

> **Alpha release** — core messaging and image viewing work, but expect rough edges. Bug reports and feedback are welcome.

## What is this?

A full WhatsApp client that runs entirely in your terminal. Read and send messages, view images — all without opening the official app or WhatsApp Web. Uses the official WhatsApp Multi-Device API, so your phone doesn't need to be online.

## Installation

### Quick install (automatic)

```bash
git clone https://github.com/DevStarByte/WhatsAppTUI.git
cd WhatsAppTUI
./run.sh --install arch    # or debian, fedora, opensuse
./run.sh
```

This installs all dependencies for your distro and builds + runs the app.

### Manual install

#### Prerequisites

| Package | Why |
|---------|-----|
| `go` (1.26+) | To build |
| `gcc` | Required for SQLite |
| `chafa` | Display images in the terminal |

```bash
# Ubuntu / Debian
sudo apt install golang gcc chafa

# Arch
sudo pacman -S go gcc chafa

# Fedora
sudo dnf install golang gcc chafa
```

#### Build & Run

```bash
git clone https://github.com/DevStarByte/WhatsAppTUI.git
cd WhatsAppTUI
go build -o whatsapp-tui .
./whatsapp-tui
```

## Setup

On first launch a QR code will appear in your terminal:

1. Open WhatsApp on your phone
2. Go to **Linked Devices** → **Link a Device**
3. Scan the QR code
4. Done — the app connects and loads your chat history

From the second launch onwards it connects automatically.

## Usage

The app has three panels: **Chat list** (left), **Messages** (right) and **Input** (bottom). Press `Tab` to switch between them.

### Navigation

| Key | Action |
|-----|--------|
| `Tab` | Switch between panels |
| `j` / `↓` | Move down |
| `k` / `↑` | Move up |
| `Enter` | Open chat |
| `/` or `Ctrl+F` | Search chats by name or number (`Enter` opens, `Tab` keeps the filter, `Esc` clears it) |
| `g` | Jump to top |
| `G` | Jump to bottom |
| `PgUp` / `PgDn` | Scroll a page (also `Ctrl+U` / `Ctrl+D` in the message panel) |
| `e` | Show / hide the edit history of edited messages (message panel) |
| Mouse wheel | Scroll messages or move through the chat list |
| `Esc` | Go back |
| `q` | Quit |

### Writing messages

| Key | Action |
|-----|--------|
| `Enter` | Send message |
| `Ctrl+W` | Delete last word |
| `Ctrl+U` | Delete to start of line |
| `Ctrl+K` | Delete to end of line |
| `Ctrl+A` / `Ctrl+E` | Move cursor to start / end |

## Chat order

Chats are sorted like on your phone: pinned chats (📌) first, most recently pinned on top, then all other chats by their latest activity. Pinning or unpinning a chat on your phone updates the list live.

## Message yourself

The chat list always contains a chat with your own number, shown as "*Your name* (You)". Use it like WhatsApp's "Message yourself" chat for notes and links. Search for `you` to find it quickly.

## Deleted and edited messages

Messages deleted by the sender are **kept** in the history and shown in red with a `deleted` tag. If the original content was never received, a red "This message was deleted" placeholder is shown instead.

Edited messages show their current text with an `edited` tag. Press `e` in the message panel to show all previous versions above the current one. Edit history is stored in `messages.db`.

## Images

Received images are automatically downloaded and displayed inline using `chafa` with symbol/braille characters. Works in any terminal that supports true color.

## Files

Everything is stored locally in the project directory:

| File | Contents |
|------|----------|
| `whatsapp.db` | Login session |
| `messages.db` | Chat history |
| `media_cache/` | Downloaded images |

To log out: delete `whatsapp.db` and restart.

## License

MIT — see [LICENSE](LICENSE). Not affiliated with WhatsApp or Meta.