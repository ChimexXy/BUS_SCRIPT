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

	// Strategy: Check API multiple times before target time
	PRELOAD_LEAD       = 30 * time.Second  // Start checking 30s early
	CHECK_INTERVAL     = 2 * time.Second   // Check every 2s for new bus
	MAX_BOOKING_TRIES  = 5                 // Try booking 5 times if it fails
	RETRY_DELAY        = 500 * time.Millisecond

	REQUEST_TIMEOUT = 3 * time.Second
	DIAL_TIMEOUT    = 2 * time.Second
)

/* ================= TYPES ================= */

type Departure struct {
	ID           int       `json:"id"`
	Locked       bool      `json:"locked"`
	NbrToHome    int       `json:"nbr_to_home"`
	NbrToCampus  int       `json:"nbr_to_campus"`
	DepartureTime string   `json:"departure_time"`
	Route        Route     `json:"route"`
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
		fmt.Println("Usage: ./bus HH:MM")
		os.Exit(1)
	}

	target, err := nextOccurrence(os.Args[1])
	if err != nil {
		fmt.Println("❌ Invalid time format")
		os.Exit(1)
	}

	fmt.Println("🎯 Target time:", target.Format("2006-01-02 15:04:05"))

	// Sleep until preload time
	sleepUntil(target.Add(-PRELOAD_LEAD), "Preload")

	fmt.Println("🔍 Starting bus monitoring...")

	var depID int
	var toCampus bool
	lastSeenID := 0

	// Keep checking for new buses until target time
	for {
		now := time.Now()
		
		// If we've reached target time, break and book
		if !now.Before(target) {
			break
		}

		// Try to get departure
		id, tc, err := getDeparture()
		if err == nil && id != 0 {
			// New bus detected!
			if id != lastSeenID {
				fmt.Printf("🆕 New bus detected! ID: %d (was: %d)\n", id, lastSeenID)
				lastSeenID = id
			}
			depID = id
			toCampus = tc
		}

		// Don't spam the API - wait before next check
		remaining := time.Until(target)
		if remaining > CHECK_INTERVAL {
			time.Sleep(CHECK_INTERVAL)
		} else if remaining > 0 {
			time.Sleep(100 * time.Millisecond)
		} else {
			break
		}
	}

	fmt.Println("🚀 BOOKING NOW!")

	// Final check if we don't have a bus yet
	if depID == 0 {
		fmt.Println("⚡ Final departure fetch...")
		if depID, toCampus, err = getDeparture(); err != nil {
			fmt.Println("❌ No available bus:", err)
			os.Exit(1)
		}
	}

	// Try booking with retries
	for attempt := 1; attempt <= MAX_BOOKING_TRIES; attempt++ {
		err := bookOnce(depID, toCampus)
		if err == nil {
			fmt.Println("✅ BUS BOOKED SUCCESSFULLY")
			return
		}

		fmt.Printf("⚠️  Booking attempt %d/%d failed: %v\n", attempt, MAX_BOOKING_TRIES, err)
		
		if attempt < MAX_BOOKING_TRIES {
			// Maybe bus ID changed, try fetching again
			newID, newTC, fetchErr := getDeparture()
			if fetchErr == nil && newID != 0 && newID != depID {
				fmt.Printf("🔄 Bus ID changed: %d -> %d, retrying...\n", depID, newID)
				depID = newID
				toCampus = newTC
			}
			time.Sleep(RETRY_DELAY)
		}
	}

	fmt.Println("❌ All booking attempts failed")
	os.Exit(1)
}

/* ================= TIME ================= */

func nextOccurrence(hhmm string) (time.Time, error) {
	loc, err := time.LoadLocation("Africa/Casablanca")
	if err != nil {
		return time.Time{}, err
	}

	t, err := time.ParseInLocation("15:04", hhmm, loc)
	if err != nil {
		return time.Time{}, err
	}

	now := time.Now().In(loc)
	target := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, loc)

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

		if remaining > time.Second {
			fmt.Printf("⏳ %s in %v\r", label, remaining.Truncate(time.Second))
			time.Sleep(500 * time.Millisecond)
		} else if remaining > 10*time.Millisecond {
			time.Sleep(2 * time.Millisecond)
		} else {
			time.Sleep(500 * time.Microsecond)
		}
	}
	fmt.Print("\n")
}

/* ================= API ================= */

func applyHeaders(req *http.Request) {
	req.Header.Set("Cookie", "le_token="+TOKEN)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Linux; Android 13)")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", "https://bus-med.1337.ma/")
	req.Header.Set("Origin", "https://bus-med.1337.ma")
}

func getDeparture() (int, bool, error) {
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

	// Find the most recent unlocked bus for our route
	var bestDep *Departure
	for i := range deps {
		d := &deps[i]
		if d.Route.Name == ROUTE && !d.Locked {
			// Prefer the newest ID (buses added later have higher IDs)
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
