package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

/* ================= CONFIG ================= */

const (
	BASE_URL = "https://bus-med.1337.ma/api"
	TOKEN    = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOjIxOSwibG9naW4iOiJtb3phaG5vdSIsImlhdCI6MTc2OTY3NjE2MCwiZXhwIjoxNzcwMjgwOTYwfQ.qKF1BZ2_iXsxLdedwO_5FFEbjWVB9o45jTr_0CyHFQ4"
	ROUTE    = "Martil"

	// Monitor for bus ID changes starting this early
	MONITOR_START      = 60 * time.Second
	CHECK_INTERVAL     = 3 * time.Second   // Check every 3s for new bus
	FINAL_CHECK_WINDOW = 5 * time.Second   // Check every 1s in final 5 seconds
	
	MAX_BOOKING_TRIES  = 5
	RETRY_DELAY        = 300 * time.Millisecond

	REQUEST_TIMEOUT = 3 * time.Second
	DIAL_TIMEOUT    = 2 * time.Second
)

/* ================= TYPES ================= */

type Departure struct {
	ID            int    `json:"id"`
	Locked        bool   `json:"locked"`
	NbrToHome     int    `json:"nbr_to_home"`
	NbrToCampus   int    `json:"nbr_to_campus"`
	DepartureTime string `json:"departure_time"`
	Route         Route  `json:"route"`
}

type Route struct {
	Name string `json:"name"`
}

type BookingRequest struct {
	DepartureID int  `json:"departure_id"`
	ToCampus    bool `json:"to_campus"`
}

/* ================= HTTP CLIENT ================= */

var httpClient = &http.Client{
	Timeout: REQUEST_TIMEOUT,
	Transport: &http.Transport{
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   5,
		IdleConnTimeout:       30 * time.Second,
		DisableKeepAlives:     false,
		DisableCompression:    false,
		ExpectContinueTimeout: 1 * time.Second,
		DialContext: (&net.Dialer{
			Timeout:   DIAL_TIMEOUT,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	},
}

/* ================= MAIN ================= */

func main() {
	if len(os.Args) != 2 {
		fmt.Println("Usage: ./bus HH:MM:SS  (e.g., ./bus 23:00:03)")
		fmt.Println("   or: ./bus HH:MM     (e.g., ./bus 23:00)")
		os.Exit(1)
	}

	target, err := nextOccurrence(os.Args[1])
	if err != nil {
		fmt.Println("❌ Invalid time format")
		fmt.Println("Use: HH:MM:SS (23:00:03) or HH:MM (23:00)")
		os.Exit(1)
	}

	fmt.Println("🎯 Target time:", target.Format("2006-01-02 15:04:05"))

	// Start monitoring early for bus ID changes
	monitorStart := target.Add(-MONITOR_START)
	sleepUntil(monitorStart, "Start monitoring")

	fmt.Println("🔍 Monitoring for bus updates...")

	var currentBusID int
	var toCampus bool
	lastSeenID := 0
	updateCount := 0

	// Keep monitoring until we reach target time
	for {
		now := time.Now()
		remaining := time.Until(target)

		// Stop monitoring when we hit target time
		if remaining <= 0 {
			break
		}

		// Fetch current bus
		busID, tc, err := getDepartureQuiet()
		if err == nil && busID != 0 {
			currentBusID = busID
			toCampus = tc

			// Notify if bus ID changed
			if busID != lastSeenID && lastSeenID != 0 {
				updateCount++
				fmt.Printf("🔄 Bus ID updated: %d -> %d (update #%d)\n", lastSeenID, busID, updateCount)
			}
			lastSeenID = busID
		}

		// Adaptive check interval: faster when close to target time
		var sleepDuration time.Duration
		if remaining <= FINAL_CHECK_WINDOW {
			sleepDuration = 500 * time.Millisecond // Check every 0.5s in final 5 seconds
		} else {
			sleepDuration = CHECK_INTERVAL // Check every 3s normally
		}

		// Show countdown periodically
		if remaining > time.Second {
			fmt.Printf("⏳ Booking in %v (Bus ID: %d)\r", remaining.Truncate(time.Second), currentBusID)
		}

		// Sleep until next check or target time
		nextCheck := now.Add(sleepDuration)
		if nextCheck.After(target) {
			sleepUntil(target, "")
			break
		}
		time.Sleep(sleepDuration)
	}

	fmt.Print("\n🚀 BOOKING NOW!\n")

	// Final pre-booking check to ensure we have latest bus ID
	finalID, finalTC, err := getDeparture()
	if err == nil && finalID != 0 {
		if finalID != currentBusID {
			fmt.Printf("⚡ Last-second update: %d -> %d\n", currentBusID, finalID)
		}
		currentBusID = finalID
		toCampus = finalTC
	}

	if currentBusID == 0 {
		fmt.Println("❌ No available bus found")
		os.Exit(1)
	}

	fmt.Printf("📍 Booking bus ID: %d\n", currentBusID)

	// Try booking with retries and dynamic ID updates
	for attempt := 1; attempt <= MAX_BOOKING_TRIES; attempt++ {
		err := bookOnce(currentBusID, toCampus)
		if err == nil {
			fmt.Println("✅ BUS BOOKED SUCCESSFULLY!")
			fmt.Printf("⏱️  Booked at: %s\n", time.Now().Format("15:04:05.000"))
			fmt.Printf("🎫 Bus ID: %d\n", currentBusID)
			return
		}

		fmt.Printf("⚠️  Attempt %d/%d failed: %v\n", attempt, MAX_BOOKING_TRIES, err)
		
		if attempt < MAX_BOOKING_TRIES {
			// Fetch fresh bus ID in case it changed during booking attempts
			newID, newTC, fetchErr := getDeparture()
			if fetchErr == nil && newID != 0 {
				if newID != currentBusID {
					fmt.Printf("🔄 Bus ID changed during retry: %d -> %d\n", currentBusID, newID)
				}
				currentBusID = newID
				toCampus = newTC
			}
			time.Sleep(RETRY_DELAY)
		}
	}

	fmt.Println("❌ All booking attempts failed")
	os.Exit(1)
}

/* ================= TIME ================= */

func nextOccurrence(hhmmss string) (time.Time, error) {
	loc, err := time.LoadLocation("Africa/Casablanca")
	if err != nil {
		return time.Time{}, err
	}

	// Try parsing with seconds first (HH:MM:SS)
	t, err := time.ParseInLocation("15:04:05", hhmmss, loc)
	if err != nil {
		// Try without seconds (HH:MM)
		t, err = time.ParseInLocation("15:04", hhmmss, loc)
		if err != nil {
			return time.Time{}, err
		}
	}

	now := time.Now().In(loc)

	target := time.Date(
		now.Year(), now.Month(), now.Day(),
		t.Hour(), t.Minute(), t.Second(), 0, loc,
	)

	if target.Before(now) {
		target = target.Add(24 * time.Hour)
	}
	return target, nil
}

func sleepUntil(t time.Time, label string) {
	for {
		remaining := time.Until(t)
		if remaining <= 0 {
			break
		}

		if label != "" && remaining > time.Second {
			fmt.Printf("⏳ %s in %v\r", label, remaining.Truncate(time.Second))
		}

		if remaining > time.Second {
			time.Sleep(100 * time.Millisecond)
		} else if remaining > 100*time.Millisecond {
			time.Sleep(10 * time.Millisecond)
		} else if remaining > 10*time.Millisecond {
			time.Sleep(time.Millisecond)
		} else {
			time.Sleep(500 * time.Microsecond)
		}
	}
	if label != "" {
		fmt.Print("\n")
	}
}

/* ================= API ================= */

func applyHeaders(req *http.Request) {
	req.Header.Set("Cookie", "le_token="+TOKEN)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 13)")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", "https://bus-med.1337.ma/")
	req.Header.Set("Origin", "https://bus-med.1337.ma")
}

// getDeparture - verbose version that prints bus info
func getDeparture() (int, bool, error) {
	id, tc, err := getDepartureQuiet()
	if err == nil && id != 0 {
		fmt.Printf("➡️  Found bus %d (%s)\n", id, ROUTE)
	}
	return id, tc, err
}

// getDepartureQuiet - silent version for monitoring loop
func getDepartureQuiet() (int, bool, error) {
	req, err := http.NewRequest("GET", BASE_URL+"/departure/current", nil)
	if err != nil {
		return 0, false, err
	}
	applyHeaders(req)

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, false, err
	}

	if len(body) > 0 && body[0] == '<' {
		return 0, false, fmt.Errorf("invalid token or blocked")
	}

	var deps []Departure
	if err := json.Unmarshal(body, &deps); err != nil {
		return 0, false, err
	}

	// Find the best unlocked bus for our route (highest ID = newest)
	var bestDep *Departure
	for i := range deps {
		d := &deps[i]
		if d.Route.Name == ROUTE && !d.Locked {
			if bestDep == nil || d.ID > bestDep.ID {
				bestDep = d
			}
		}
	}

	if bestDep != nil {
		return bestDep.ID, false, nil
	}

	return 0, false, fmt.Errorf("no %s bus found", ROUTE)
}

/* ================= BOOKING ================= */

func bookOnce(depID int, toCampus bool) error {
	payload := BookingRequest{
		DepartureID: depID,
		ToCampus:    toCampus,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", BASE_URL+"/tickets/book", bytes.NewBuffer(data))
	if err != nil {
		return err
	}
	applyHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusCreated {
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
}
