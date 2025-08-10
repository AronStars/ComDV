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

// TileCache ----- State -----
type TileCache struct {
	mu sync.RWMutex
	m  map[string]image.Image
}

type AppState struct {
	CenterLon, CenterLat float64
	Zoom                 int // ADDED: The map's zoom level
	Crimes               []CrimePoint
	mu                   sync.RWMutex
	tileCache            *TileCache // ADDED: A pointer to the tile cache
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
func drawCrimes(img *image.RGBA, w, h int, centerLon, centerLat float64, zoom int, crimes []CrimePoint) {
	if len(crimes) == 0 {
		return
	}
	cx, cy := w/2, h/2

	// The number of pixels for 360 degrees of longitude at the equator for a given zoom level
	// is 256 * (2^zoom). We use this to calculate a dynamic scaling factor.
	pixelsPer360Deg := 256.0 * math.Pow(2, float64(zoom))
	pixelsPerDegLon := pixelsPer360Deg / 360.0

	// Latitude scaling is adjusted by the cosine of the latitude
	pixelsPerDegLat := pixelsPerDegLon * math.Cos(centerLat*math.Pi/180)

	for _, c := range crimes {
		dxDeg := c.Lon - centerLon
		dyDeg := c.Lat - centerLat
		px := cx + int(dxDeg*pixelsPerDegLon)
		py := cy - int(dyDeg*pixelsPerDegLat) // Y is inverted in screen coordinates

		// Draw a small red circle for the crime
		radius := 4
		for yOff := -radius; yOff <= radius; yOff++ {
			for xOff := -radius; xOff <= radius; xOff++ {
				if xOff*xOff+yOff*yOff <= radius*radius {
					X, Y := px+xOff, py+yOff
					if image.Pt(X, Y).In(img.Bounds()) {
						img.Set(X, Y, color.RGBA{R: 200, G: 0, B: 0, A: 220})
					}
				}
			}
		}
	}
}

// converts longitude and latitude to floating-point tile numbers at a given zoom.
func lonLatToTile(lon, lat float64, zoom int) (x, y float64) {
	latRad := lat * math.Pi / 180
	n := math.Pow(2, float64(zoom))
	x = (lon + 180.0) / 360.0 * n
	y = (1.0 - math.Log(math.Tan(latRad)+1.0/math.Cos(latRad))/math.Pi) / 2.0 * n
	return x, y
}

func getTile(z, x, y int) image.Image {
	// Create a unique key for this tile
	tileKey := fmt.Sprintf("%d/%d/%d", z, x, y)

	// Check the cache first (with a read lock)
	st.tileCache.mu.RLock()
	img, found := st.tileCache.m[tileKey]
	st.tileCache.mu.RUnlock()
	if found {
		return img
	}

	// Not in cache, download it.
	// Set a valid User-Agent per OpenStreetMap policy.
	url := fmt.Sprintf("https://tile.openstreetmap.org/%s.png", tileKey)
	client := &http.Client{}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "AronG-CrimeMapper/0.1")

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Failed to download tile %s: %v", tileKey, err)
		return nil // Or return a placeholder "error" image
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {

		}
	}(resp.Body)

	img, _, err = image.Decode(resp.Body)
	if err != nil {
		log.Printf("Failed to decode tile %s: %v", tileKey, err)
		return nil // Or return a placeholder "error" image
	}

	// Add the newly downloaded image to the cache (with a write lock)
	st.tileCache.mu.Lock()
	st.tileCache.m[tileKey] = img
	st.tileCache.mu.Unlock()

	return img
}

// ----- Main -----
func main() {
	st = AppState{
		Zoom: 14, // A good default zoom level for a city
		tileCache: &TileCache{
			m: make(map[string]image.Image),
		},
	}

	a := app.NewWithID("com.arong.crimemapper")
	w := a.NewWindow("Community Data Visualiser")
	w.Resize(fyne.NewSize(1100, 700))

	// Map canvas first so closures can capture it
	var mapCanvas *canvas.Raster
	mapCanvas = canvas.NewRaster(func(w, h int) image.Image {
		// Create the image we will draw on
		img := image.NewRGBA(image.Rect(0, 0, w, h))

		// Get a read-lock on the state for consistent values during this draw cycle
		st.mu.RLock()
		zoom := st.Zoom
		centerLon := st.CenterLon
		centerLat := st.CenterLat
		crimes := st.Crimes
		st.mu.RUnlock()

		if centerLon == 0 && centerLat == 0 {
			// Don't draw a map if no location is set. Just draw a message.
			draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 240, G: 240, B: 240, A: 255}}, image.Point{}, draw.Src)
			return img // Return early
		}

		// 1. Convert our map's center Lon/Lat to a floating-point tile coordinate
		centerXTile, centerYTile := lonLatToTile(centerLon, centerLat, zoom)

		// 2. Calculate the top-left tile we need to start drawing from
		// The center of the screen in pixels is (w/2, h/2)
		// The center of the map in tile units is (centerXTile, centerYTile)
		// We calculate the top-left of the screen in tile units
		startTileX := centerXTile - (float64(w)/2)/256.0
		startTileY := centerYTile - (float64(h)/2)/256.0

		// 3. Loop to draw all visible tiles
		// We iterate from the start tile to fill the whole screen width and height
		for y := 0.0; y < float64(h); y += 256.0 {
			for x := 0.0; x < float64(w); x += 256.0 {
				// Calculate which tile (integer) corresponds to this screen pixel
				tileX := int(math.Floor(startTileX + x/256.0))
				tileY := int(math.Floor(startTileY + y/256.0))

				// Fetch the tile from our cache/downloader
				tileImg := getTile(zoom, tileX, tileY)
				if tileImg != nil {
					// Calculate exactly where on our canvas to draw this tile
					// This accounts for the fractional part of our startTile coordinates
					drawX := int(math.Round((float64(tileX) - startTileX) * 256.0))
					drawY := int(math.Round((float64(tileY) - startTileY) * 256.0))

					// Draw the tile onto our main image
					draw.Draw(img, image.Rect(drawX, drawY, drawX+256, drawY+256), tileImg, image.Point{}, draw.Src)
				}
			}
		}

		// 4. Draw overlays (crosshair and crimes) on top of the map
		// The logic for drawing crimes now needs the zoom level to scale correctly
		drawCrimes(img, w, h, centerLon, centerLat, zoom, crimes)

		// Draw the crosshair
		cx, cy := w/2, h/2
		for dx := -8; dx <= 8; dx++ {
			img.Set(cx+dx, cy, color.Black)
			img.Set(cx+dx, cy-1, color.White)
			img.Set(cx+dx, cy+1, color.White)
		}
		for dy := -8; dy <= 8; dy++ {
			img.Set(cx, cy+dy, color.Black)
			img.Set(cx-1, cy+dy, color.White)
			img.Set(cx+1, cy+dy, color.White)
		}

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
