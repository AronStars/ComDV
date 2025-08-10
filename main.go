package main

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// ----- State -----
type AppState struct {
	CenterLon, CenterLat float64
	Crimes               []CrimePoint
	mu                   sync.RWMutex // Added a mutex for safe concurrent access
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
	// Simplified the defer and error check
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("Warning: failed to close response body: %v", closeErr)
		}
	}()

	var dec postcodeIOResp
	if err := json.NewDecoder(resp.Body).Decode(&dec); err != nil {
		return 0, 0, err
	}
	if dec.Status != 200 {
		return 0, 0, fmt.Errorf("postcode not found or API error (status %d)", dec.Status)
	}
	return dec.Result.Longitude, dec.Result.Latitude, nil
}

// ----- Police API (unchanged, with minor fix) -----
type policeCrime struct {
	Category string `json:"category"`
	Location struct {
		Latitude  string `json:"latitude"`
		Longitude string `json:"longitude"`
	} `json:"location"`
}

// latestYYYYMM is a fallback if the API doesn't respond
func latestYYYYMM() string {
	// The police API data is often 2-3 months behind. Let's default to -3.
	y, m, _ := time.Now().AddDate(0, -3, 0).Date()
	return fmt.Sprintf("%04d-%02d", y, int(m))
}

func latestAvailableYYYYMM(ctx context.Context) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://data.police.uk/api/crime-last-updated", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			log.Printf("Warning: failed to close response body: %v", err)
		}
	}(resp.Body)

	var r struct {
		Date string `json:"date"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return "", err
	}

	// The API can return different formats, e.g., a full timestamp or just a date.
	// try parsing them in order of preference.
	// time.RFC3339-> "2006-01-02T15:04:05Z07:00"
	// "2006-01-02"-> "YYYY-MM-DD"
	possibleLayouts := []string{
		time.RFC3339,
		"2006-01-02",
	}

	var t time.Time
	var parseErr error

	for _, layout := range possibleLayouts {
		t, parseErr = time.Parse(layout, r.Date)
		if parseErr == nil {
			// Successfully parsed, break the loop.
			break
		}
	}

	// If after trying all layouts, still have an error, then fail.
	if parseErr != nil {
		return "", fmt.Errorf("could not parse date '%s': %w", r.Date, parseErr)
	}

	// Successfully parsed, return in the required YYYY-MM format.
	return fmt.Sprintf("%04d-%02d", t.Year(), int(t.Month())), nil
}

func fetchCrimes(ctx context.Context, lon, lat float64, yyyymm string) ([]CrimePoint, error) {
	url := fmt.Sprintf("https://data.police.uk/api/crimes-street/all-crime?lat=%f&lng=%f&date=%s", lat, lon, yyyymm)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	// CHANGED: Do not panic on close error.
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Printf("Warning: failed to close response body: %v", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API request failed with status: %s", resp.Status)
	}

	var raw []policeCrime
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	out := make([]CrimePoint, 0, len(raw))
	for _, r := range raw {
		la, err1 := strconv.ParseFloat(r.Location.Latitude, 64)
		lo, err2 := strconv.ParseFloat(r.Location.Longitude, 64)
		if err1 == nil && err2 == nil {
			out = append(out, CrimePoint{Lon: lo, Lat: la, Category: r.Category})
		}
	}
	return out, nil
}

// ----- Render helpers -----
func drawCrimes(img *image.RGBA, w, h int, centerLon, centerLat float64, crimes []CrimePoint) {
	if len(crimes) == 0 {
		return
	}
	cx, cy := w/2, h/2

	for _, c := range crimes {
		dxDeg := c.Lon - centerLon
		dyDeg := c.Lat - centerLat
		py := cy - int(dyDeg*20000.0)
		px := cx + int(dxDeg*20000.0*math.Cos(centerLat*math.Pi/180))

		for y := -3; y <= 3; y++ {
			for x := -3; x <= 3; x++ {
				if x*x+y*y <= 9 {
					X, Y := px+x, py+y
					if image.Pt(X, Y).In(img.Bounds()) {
						img.Set(X, Y, color.RGBA{R: 200, G: 0, B: 0, A: 220})
					}
				}
			}
		}
	}
}

// ----- Main -----
func main() {
	a := app.NewWithID("com.arong.crimemapper")
	w := a.NewWindow("Community Data Visualiser")
	w.Resize(fyne.NewSize(1100, 700))

	// Map canvas first so closures can capture it
	var mapCanvas *canvas.Raster
	mapCanvas = canvas.NewRaster(func(wd, ht int) image.Image {
		img := image.NewRGBA(image.Rect(0, 0, wd, ht))
		draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 245, G: 245, B: 245, A: 255}}, image.Point{}, draw.Src)

		// Use a read-lock to safely access state for drawing
		st.mu.RLock()
		defer st.mu.RUnlock()

		// crosshair
		cx, cy := wd/2, ht/2
		if st.CenterLon != 0 { // Only draw crosshair if a center is set
			for dx := -6; dx <= 6; dx++ {
				img.Set(cx+dx, cy, color.RGBA{A: 255})
			}
			for dy := -6; dy <= 6; dy++ {
				img.Set(cx, cy+dy, color.RGBA{A: 255})
			}
		}
		// crimes overlay
		drawCrimes(img, wd, ht, st.CenterLon, st.CenterLat, st.Crimes)
		return img
	})

	// Controls
	postcode := widget.NewEntry()
	postcode.SetPlaceHolder("e.g. SW1A 1AA")

	statusLabel := widget.NewLabel("Enter a postcode to begin.") // REFACTORED: Add a status label

	//  New function to handle crime fetching logic
	updateCrimes := func() {
		go func() {
			st.mu.RLock()
			lon := st.CenterLon
			lat := st.CenterLat
			st.mu.RUnlock()

			if lon == 0 && lat == 0 {
				return // Don't fetch for the default location
			}

			fyne.Do(func() { statusLabel.SetText("Fetching crime data...") })

			yyyymm, errMonth := latestAvailableYYYYMM(context.Background())
			if errMonth != nil {
				yyyymm = latestYYYYMM() // fallback
			}

			pts, err := fetchCrimes(context.Background(), lon, lat, yyyymm)
			if err != nil {
				fyne.Do(func() {
					fyne.CurrentApp().SendNotification(&fyne.Notification{
						Title:   "Crime fetch error",
						Content: err.Error(),
					})
					statusLabel.SetText("Error fetching crime data.")
				})
				return
			}

			st.mu.Lock()
			st.Crimes = pts
			st.mu.Unlock()

			log.Printf("Fetched %d crimes for %s", len(pts), yyyymm)
			fyne.Do(func() {
				msg := fmt.Sprintf("%d crime points loaded for %s.", len(pts), yyyymm)
				statusLabel.SetText(msg)
				fyne.CurrentApp().SendNotification(&fyne.Notification{
					Title:   "Crimes loaded",
					Content: msg,
				})
				canvas.Refresh(mapCanvas)
			})
		}()
	}

	showCrimes := widget.NewCheck("Show Crimes (latest available month)", func(on bool) {
		if !on {
			st.mu.Lock()
			st.Crimes = nil
			st.mu.Unlock()
			canvas.Refresh(mapCanvas)
			statusLabel.SetText("Crime overlay off.")
			return
		}
		// If toggled on, just call function
		updateCrimes()
	})
	showCrimes.Disable() // Start with the checkbox disabled

	goBtn := widget.NewButton("Go", func() {
		fyne.Do(func() { statusLabel.SetText("Finding postcode...") })
		go func() {
			lon, lat, err := geocodePostcode(context.Background(), postcode.Text)
			if err != nil {
				fyne.Do(func() {
					fyne.CurrentApp().SendNotification(&fyne.Notification{
						Title:   "Geocode error",
						Content: err.Error(),
					})
					statusLabel.SetText("Could not find postcode.")
				})
				return
			}

			st.mu.Lock()
			st.CenterLon, st.CenterLat = lon, lat
			st.mu.Unlock()

			fyne.Do(func() {
				fyne.CurrentApp().SendNotification(&fyne.Notification{
					Title:   "Centered",
					Content: fmt.Sprintf("Lon: %.5f, Lat: %.5f", lon, lat),
				})
				showCrimes.Enable() // Enable checkbox now that there is a location
				statusLabel.SetText(fmt.Sprintf("Centered on Lon: %.3f, Lat: %.3f", lon, lat))
				canvas.Refresh(mapCanvas)
			})

			// FIXED: If crimes are toggled on, refetch for new centre
			if showCrimes.Checked {
				updateCrimes()
			}
		}()
	})

	left := container.NewVBox(
		widget.NewLabel("Find by postcode"),
		container.NewBorder(nil, nil, nil, goBtn, postcode), // Put button next to entry
		widget.NewSeparator(),
		showCrimes,
		statusLabel,
	)

	w.SetContent(container.NewHSplit(left, mapCanvas))
	w.ShowAndRun()
}
