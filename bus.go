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

	PRELOAD_LEAD       = 10 * time.Second
	MAX_BOOKING_TRIES  = 3
	RETRY_DELAY        = 200 * time.Millisecond

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

	fmt.Println("🎯 Target time:", target.Format("2006-01-02 15:04:05.000"))

	sleepUntil(target.Add(-PRELOAD_LEAD), "Preload")

	fmt.Println("⚙️  Prefetching departure...")
	depID, toCampus, _ := getDeparture()

	sleepUntil(target, "Booking")

	fmt.Println("🚀 BOOKING NOW!")

	// Final fetch if we don't have a bus
	if depID == 0 {
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
			fmt.Println("⏱️  Booked at:", time.Now().Format("15:04:05.000"))
			return
		}

		fmt.Printf("⚠️  Attempt %d/%d failed: %v\n", attempt, MAX_BOOKING_TRIES, err)
		
		if attempt < MAX_BOOKING_TRIES {
			// Try fetching fresh bus ID
			newID, newTC, fetchErr := getDeparture()
			if fetchErr == nil && newID != 0 {
				if newID != depID {
					fmt.Printf("🔄 New bus ID: %d -> %d\n", depID, newID)
				}
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

		if remaining > time.Second {
			fmt.Printf("⏳ %s in %v\r", label, remaining.Truncate(time.Second))
			time.Sleep(100 * time.Millisecond)
		} else if remaining > 100*time.Millisecond {
			// Fine-grained sleep when close
			time.Sleep(10 * time.Millisecond)
		} else if remaining > 10*time.Millisecond {
			time.Sleep(time.Millisecond)
		} else {
			// Busy wait for final milliseconds (most accurate)
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
		fmt.Printf("➡️  Found bus %d (%s)\n", bestDep.ID, ROUTE)
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
