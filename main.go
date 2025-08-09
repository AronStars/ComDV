package main

import (
	"image"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

func main() {
	a := app.New()
	w := a.NewWindow("Community Data Visualiser – Crime")
	w.Resize(fyne.NewSize(1100, 700))

	// Left controls
	postcode := widget.NewEntry()
	postcode.SetPlaceHolder("e.g. SW1A 1AA")
	month := widget.NewSelect([]string{ /* we’ll fill this later */ }, nil)
	month.PlaceHolder = "Latest month"
	showCrimes := widget.NewCheck("Crimes (last month)", nil)
	goBtn := widget.NewButton("Go", func() {
		// TODO: geocode, fetch crimes, refresh map
	})

	left := container.NewVBox(
		widget.NewLabel("Find by postcode"),
		postcode,
		month,
		showCrimes,
		goBtn,
	)

	// Right map canvas placeholder
	mapCanvas := canvas.NewRaster(func(w, h int) image.Image {
		// TODO: draw tiles + markers; for now a blank background
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		return img
	})

	w.SetContent(container.NewHSplit(left, mapCanvas))
	w.ShowAndRun()
}
