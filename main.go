package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"net/http"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// AppState ----- State -----
type AppState struct {
	CenterLon, CenterLat float64
	Crimes               []CrimePoint // will fill later
}

type CrimePoint struct {
	Lon, Lat float64
	Category string
}

var st AppState

// ----- API types -----
type postcodeIOResp struct {
	Status int `json:"status"`
	Result struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"result"`
}

// ----- Geocoding -----
func geocodePostcode(ctx context.Context, pc string) (lon, lat float64, err error) {
	pc = strings.ToUpper(strings.TrimSpace(pc))
	pc = strings.ReplaceAll(pc, " ", "")
	url := "https://api.postcodes.io/postcodes/" + pc

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {

		}
	}(resp.Body)

	var dec postcodeIOResp
	if err := json.NewDecoder(resp.Body).Decode(&dec); err != nil {
		return 0, 0, err
	}
	if dec.Status != 200 {
		return 0, 0, fmt.Errorf("postcode not found or API error (status %d)", dec.Status)
	}
	return dec.Result.Longitude, dec.Result.Latitude, nil
}

func main() {
	a := app.New()
	w := a.NewWindow("Community Data Visualiser")
	w.Resize(fyne.NewSize(1100, 700))

	// Left controls
	postcode := widget.NewEntry()
	postcode.SetPlaceHolder("e.g. SW1A 1AA")
	month := widget.NewSelect([]string{}, nil) // fill later
	month.PlaceHolder = "Latest month"
	showCrimes := widget.NewCheck("Crimes (last month)", nil)

	// Declare mapCanvas first so the button can reference it
	var mapCanvas *canvas.Raster
	mapCanvas = canvas.NewRaster(func(wd, ht int) image.Image {
		img := image.NewRGBA(image.Rect(0, 0, wd, ht))
		// plain background
		draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 245, G: 245, B: 245, A: 255}}, image.Point{}, draw.Src)
		// crosshair at visual center
		cx, cy := wd/2, ht/2
		for dx := -6; dx <= 6; dx++ {
			img.Set(cx+dx, cy, color.RGBA{A: 255})
		}
		for dy := -6; dy <= 6; dy++ {
			img.Set(cx, cy+dy, color.RGBA{A: 255})
		}
		return img
	})

	// Button AFTER mapCanvas exists (or declared above)
	goBtn := widget.NewButton("Go", func() {
		go func() { // run in background
			lon, lat, err := geocodePostcode(context.Background(), postcode.Text)
			if err != nil {
				fyne.CurrentApp().SendNotification(&fyne.Notification{
					Title:   "Geocode error",
					Content: err.Error(),
				})
				return
			}
			st.CenterLon, st.CenterLat = lon, lat

			// Just refresh the canvas
			fyne.Do(func() {
				canvas.Refresh(mapCanvas)
			})
		}()
	})

	left := container.NewVBox(
		widget.NewLabel("Find by postcode"),
		postcode,
		month,
		showCrimes,
		goBtn,
	)

	w.SetContent(container.NewHSplit(left, mapCanvas))
	w.ShowAndRun()
}
