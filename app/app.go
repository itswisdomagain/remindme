package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/decred/dcrd/dcrutil/v3"
	"go.etcd.io/bbolt"

	"fyne.io/fyne"
	"fyne.io/fyne/app"
	"fyne.io/fyne/canvas"
	"fyne.io/fyne/layout"
	"fyne.io/fyne/theme"
	"fyne.io/fyne/widget"
)

// Declaring these here to enable button click handlers
// have access to them.
var (
	a          = app.New()
	mainWindow = a.NewWindow("RemindMe")

	activeReminders = make(map[string][]*Item)
	lastRunStatuses = make(map[string]int)

	categoryEntry      *widget.Select
	activeRemindersBox *fyne.Container
)

func main() {
	appDataDir := dcrutil.AppDataDir("remindme", false)
	err := os.MkdirAll(appDataDir, 0700)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create app data directory: %v\n", err)
		os.Exit(1)
	}

	dbPath := filepath.Join(appDataDir, "app.db")
	db, err = bbolt.Open(dbPath, 0600, &bbolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open database: %v\n", err)
		os.Exit(1)
	}

	categories, err := categoriesFromDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to fetch categories: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Start a goroutine to catch interrupt signal (e.g. ctrl+c)
	// before starting the api server. On interrupt, kill the
	// ctx associated with the api server to signal the api
	// server to stop.
	killChan := make(chan os.Signal)
	signal.Notify(killChan, os.Interrupt)
	go func() {
		for range killChan {
			fmt.Println("shutting down...")
			cancel()
			a.Quit()
			return
		}
	}()

	a.Settings().SetTheme(theme.LightTheme())
	mainWindow.CenterOnScreen()
	mainWindow.Resize(fyne.NewSize(500, 300))

	categoryEntry = widget.NewSelect(categories, nil)
	activeRemindersBox = fyne.NewContainerWithLayout(layout.NewVBoxLayout())

	errorLabel := widget.NewLabel("")
	errorLabel.Hide()

	refreshCategories := func() {
		errorLabel.SetText("Refreshing...")
		errorLabel.Show()

		categories, err := downloadFromAPI()
		if err != nil {
			errorLabel.SetText(err.Error())
			return
		}
		categoryEntry.Options = categories
		for category := range activeReminders {
			items, err := categoryItems(category)
			if err != nil {
				errorLabel.SetText(err.Error())
				return
			}
			activeReminders[category] = items
		}
		errorLabel.Hide()
	}
	refreshCategories()

	noDelayCheck := widget.NewCheck("No initial delay", nil)

	startButton := widget.NewButton("Start", func() {
		if categoryEntry.SelectedIndex() < 0 {
			errorLabel.SetText("Please select a reminder category")
			errorLabel.Show()
			return
		}

		selectedCategory := categoryEntry.Selected
		if _, active := activeReminders[selectedCategory]; active {
			errorLabel.SetText("Already running reminders for " + selectedCategory)
			errorLabel.Show()
			return
		}

		items, err := categoryItems(selectedCategory)
		if err != nil {
			errorLabel.SetText(err.Error())
			errorLabel.Show()
			return
		}
		activeReminders[selectedCategory] = items

		errorLabel.Hide()
		startTimer(noDelayCheck.Checked, selectedCategory, ctx)
		categoryEntry.SetSelectedIndex(-1)
	})

	mainWindow.SetContent(widget.NewVBox(
		widget.NewHBox(layout.NewSpacer(), widget.NewButton("Refresh", refreshCategories)),
		errorLabel,
		widget.NewLabelWithStyle("Reminder categories:", fyne.TextAlignLeading, fyne.TextStyle{Italic: true}),
		categoryEntry,
		noDelayCheck,
		startButton,
		widget.NewLabelWithStyle("Active reminders", fyne.TextAlignLeading, fyne.TextStyle{Italic: true}),
		activeRemindersBox,
	))

	lastRunStatuses, err = lastRuns()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to fetch last runs: %v\n", err)
		lastRunStatuses = make(map[string]int)
	}
	for category := range lastRunStatuses {
		items, err := categoryItems(category)
		if err != nil {
			fmt.Println("failed to fetch items for resumed category ", category, err.Error())
			delete(lastRunStatuses, category)
			continue
		}
		activeReminders[category] = items
		startTimer(false, category, ctx)
	}

	mainWindow.SetCloseIntercept(a.Quit)
	mainWindow.ShowAndRun()
}

func startTimer(immediateDisplay bool, category string, mainCtx context.Context) {
	items, exist := activeReminders[category]
	if !exist || len(items) == 0 {
		return
	}
	lastIndex, exist := lastRunStatuses[category]
	if !exist {
		lastIndex = -1
	}
	remaining := len(items) - lastIndex - 1
	if remaining == 0 {
		return
	}

	activeLabel := widget.NewLabel(fmt.Sprintf("%s (%d)", category, remaining))

	newReminder := widget.NewHBox()
	killReminder := func() {
		activeRemindersBox.Remove(newReminder)
		mainWindow.Canvas().Refresh(activeRemindersBox)
		delete(activeReminders, category)
		delete(lastRunStatuses, category)
		if err := deleteLastRun(category); err != nil {
			fmt.Println("error deleting last run for ", category, err.Error())
		}
	}

	ctx, cancel := context.WithCancel(mainCtx)
	go func() {
		if immediateDisplay && !showReminder(category, activeLabel) { // show first reminder before starting timer, if immediateDisplay=true
			return
		}
		ticker := time.NewTicker(15 * time.Second)
		defer func() {
			ticker.Stop()
			killReminder()
		}()
		for {
			select {
			case <-ticker.C:
				if !showReminder(category, activeLabel) {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	newReminder.Append(activeLabel)
	newReminder.Append(layout.NewSpacer())
	newReminder.Append(widget.NewButton("X", cancel))
	activeRemindersBox.Add(newReminder)
}

func showReminder(category string, catLabel *widget.Label) bool {
	items, exist := activeReminders[category]
	if !exist || len(items) == 0 {
		return false // no items to display, kill ticker
	}
	lastIndex, exist := lastRunStatuses[category]
	if !exist {
		lastIndex = -1
	}

	nextIndex := lastIndex + 1
	if nextIndex >= len(items) {
		return false // reached the end, kill ticker
	}

	lastRunStatuses[category] = nextIndex
	if err := saveLastRun(category, nextIndex); err != nil {
		fmt.Println("error saving last run record for", category, err.Error())
	}
	nextItem := items[nextIndex]

	var itemUI fyne.CanvasObject
	var imgSize image.Point
	switch strings.ToLower(nextItem.Type) {
	case "text":
		text := string(nextItem.Content)
		label := widget.NewLabel(text)
		label.Wrapping = fyne.TextWrapWord
		itemUI = label

	case "image":
		imgReader := bytes.NewReader(nextItem.Content)
		img, _, err := image.Decode(imgReader)
		if err != nil {
			println(err.Error())
			itemUI = widget.NewLabelWithStyle("Error displaying image: "+nextItem.Name, fyne.TextAlignCenter, fyne.TextStyle{Italic: true})
		} else {
			itemUI = canvas.NewImageFromImage(img)
			imgSize = img.Bounds().Size()
		}

	case "link":
		text := string(nextItem.Content)
		link, err := url.Parse(text)
		if err != nil {
			println(err.Error())
			itemUI = widget.NewLabel(text)
		} else {
			itemUI = widget.NewHyperlink(text, link)
		}

	default:
		itemUI = widget.NewLabelWithStyle("This is a/an "+nextItem.Type, fyne.TextAlignCenter, fyne.TextStyle{Italic: true})
	}

	w := a.NewWindow(category + ": " + nextItem.Name)
	w.SetContent(itemUI)
	winSize := w.Canvas().Size()
	if strings.ToLower(nextItem.Type) == "image" {
		winSize = fyne.NewSize(imgSize.X, imgSize.Y)
	}
	if winSize.Height < 200 {
		winSize.Height = 200
	} else if winSize.Height > 400 {
		winSize.Height = 400
	}
	if winSize.Width < 300 {
		winSize.Width = 300
	} else if winSize.Width > 600 {
		winSize.Width = 600
	}
	w.Resize(winSize)
	w.Show()

	remaining := len(items) - nextIndex - 1
	catLabel.SetText(fmt.Sprintf("%s (%d)", category, remaining))
	return remaining > 0 // only return true if there's more to show
}
