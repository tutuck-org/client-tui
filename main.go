package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	core "github.com/tutuck-org/client-core"
)

var Version string

var identity struct {
	Name string
	ID   int
}

var (
	colorGreen    = "#5f875f"
	colorOrange   = "#d7af87"
	colorViolet   = "#af87d7"
	colorBlueGray = "#87afd7"
	colorRed      = "#d78787"
)

func runTUI(server string, conn io.ReadWriteCloser) {
	var mu sync.Mutex

	app := tview.NewApplication()
	chatView := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWrap(true).
		SetChangedFunc(func() { app.Draw() })

	messageField := tview.NewInputField().
		SetLabel("> ").
		SetFieldWidth(0).
		SetFieldBackgroundColor(tcell.NewRGBColor(50, 50, 50))

	var history []string
	historyIndex := -1
	fields := []tview.Primitive{messageField, chatView}
	focusIndex := 0
	app.SetFocus(fields[focusIndex])
	var lastEscTime int64

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		now := time.Now().UnixNano() / 1e6
		if event.Key() == tcell.KeyEscape {
			if now-lastEscTime < 500 {
				app.Stop()
				return nil
			}
			lastEscTime = now
			return nil
		}

		switch event.Key() {
		case tcell.KeyTab:
			focusIndex = (focusIndex + 1) % len(fields)
			app.SetFocus(fields[focusIndex])
			return nil
		case tcell.KeyBacktab:
			focusIndex = (focusIndex - 1 + len(fields)) % len(fields)
			app.SetFocus(fields[focusIndex])
			return nil
		}

		if fields[focusIndex] == chatView {
			row, _ := chatView.GetScrollOffset()
			switch event.Key() {
			case tcell.KeyUp:
				if row > 0 {
					chatView.ScrollTo(row-1, 0)
				}
			case tcell.KeyDown:
				chatView.ScrollTo(row+1, 0)
			case tcell.KeyRune:
				switch event.Rune() {
				case 'k', 'p':
					if row > 0 {
						chatView.ScrollTo(row-1, 0)
					}
				case 'j', 'n':
					chatView.ScrollTo(row+1, 0)
				}
			}
		}

		if fields[focusIndex] == messageField {
			switch event.Key() {
			case tcell.KeyUp:
				if len(history) > 0 && historyIndex+1 < len(history) {
					historyIndex++
					messageField.SetText(history[len(history)-1-historyIndex])
				}
			case tcell.KeyDown:
				if historyIndex > 0 {
					historyIndex--
					messageField.SetText(history[len(history)-1-historyIndex])
				} else {
					historyIndex = -1
					messageField.SetText("")
				}
			}
		}

		return event
	})

	inputFlex := tview.NewFlex().
		AddItem(messageField, 0, 1, true)

	sendMessage := func() {
		mu.Lock()
		defer mu.Unlock()
		if conn == nil {
			return
		}
		text := strings.TrimSpace(messageField.GetText())
		if text == "" {
			return
		}

		history = append(history, text)
		historyIndex = -1

		if _, err := conn.Write([]byte(text)); err != nil {
			fmt.Fprintf(chatView, "%sWrite error: %s[-]\n", colorRed, err)
		}
		chatView.ScrollToEnd()
		messageField.SetText("")
	}

	messageField.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter {
			sendMessage()
		}
	})

	flex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(chatView, 0, 1, false).
		AddItem(inputFlex, 1, 1, true)
	app.SetRoot(flex, true).EnableMouse(true)

	go func() {
		dec := json.NewDecoder(conn)

		for {
			if conn == nil {
				for i := 1; i <= 10; i++ {
					newConn, err := core.ConnectSSH(server)
					if err == nil {
						conn = newConn
						app.QueueUpdateDraw(func() {
							fmt.Fprintf(chatView, "[%s]Reconnected![-]\n", colorGreen)
							chatView.ScrollToEnd()
							dec = json.NewDecoder(conn)
						})
						break
					}
					app.QueueUpdateDraw(func() {
						fmt.Fprintf(chatView, "[%s]Reconnect attempt %d failed: %v[-]\n", colorRed, i, err)
						chatView.ScrollToEnd()
					})
					time.Sleep(2 * time.Second)
				}
				if conn == nil {
					app.QueueUpdateDraw(func() {
						fmt.Fprintf(chatView, "[%s]Cannot reconnect after 10 attempts, exiting...[-]\n", colorRed)
						chatView.ScrollToEnd()
					})
					time.Sleep(1 * time.Second)
					app.Stop()
					return
				}
			}

			var p core.Packet

			err := dec.Decode(&p)
			if err != nil {
				app.QueueUpdateDraw(func() {
					fmt.Fprintf(chatView, "[%s]Server disconnected, reconnecting...[-]\n", colorRed)
					chatView.ScrollToEnd()
				})
				conn.Close()
				conn = nil
				time.Sleep(500 * time.Millisecond)
				continue
			}

			var msg string

			switch p.Type {
			case "identity":
				identity.Name = p.Name
				identity.ID = p.ID
			case "message":
				if p.Scope == "dm" {
					msg = fmt.Sprintf("⟨[%s]%s[-]⟩ [%s]%s[-] -> [%s]%s[-] | %s", colorGreen, p.Time, p.FromColor, p.From, p.ToColor, p.To, p.Content)
				}
				if p.Scope == "global" {
					msg = fmt.Sprintf("⟨[%s]%s[-]⟩ [%s]%s[-] | %s", colorGreen, p.Time, p.FromColor, p.From, p.Content)
				}
			case "system":
				msg = fmt.Sprintf("%s", p.Content)
			case "error":
				msg = fmt.Sprintf("[%s]%s[-]", colorRed, p.Content)
			case "color":
				msg = fmt.Sprintf("%2d) [%s]%s[-] (%s)", p.Num, p.ColorHex, p.ColorName, p.ColorHex)
			}

			app.QueueUpdateDraw(func() {
				if msg == "" {
					return
				}

				fmt.Fprintf(chatView, "%s\n", msg)
				chatView.ScrollToEnd()
				app.SetFocus(messageField)
			})
		}
	}()

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Printf("Usage:\n  %s generate         # generate SSH keys\n  %s <address:port>    # connect to server\n", os.Args[0], os.Args[0])
		return
	}

	cmd := os.Args[1]

	if cmd == "generate" {
		if err := core.GenerateKeys(); err != nil {
			fmt.Println("Error:", err)
		}
		return
	}

	var server string
	server = cmd

	conn, err := core.ConnectSSH(server)
	if err != nil {
		fmt.Println("Connection error:", err)
		return
	}
	defer conn.Close()

	runTUI(server, conn)
}
