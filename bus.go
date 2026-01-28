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

	// 🔴 MUST be fresh & valid
	TOKEN = "PUT_YOUR_TOKEN_HERE"

	ROUTE = "Martil" // or "Tetouan"

	PRELOAD_LEAD    = 10 * time.Second
	REQUEST_TIMEOUT = 6 * time.Second
)

/* ================= TYPES ================= */

type Departure struct {
	ID          int   `json:"id"`
	Locked      bool  `json:"locked"`
	NbrToHome   int   `json:"nbr_to_home"`
	NbrToCampus int   `json:"nbr_to_campus"`
	Route       Route `json:"route"`
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
		ForceAttemptHTTP2: false,
		DialContext: (&net.Dialer{
			Timeout: 4 * time.Second,
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

	sleepUntil(target.Add(-PRELOAD_LEAD), "Preload")

	fmt.Println("⚙️  Fetching departure…")
	depID, toCampus, _ := getDeparture() // preload may fail (normal)

	sleepUntil(target, "Booking")

	fmt.Println("🚀 BOOKING")

	if depID == 0 {
		depID, toCampus, err = getDeparture()
		if err != nil {
			fmt.Println("❌ No available seats:", err)
			os.Exit(1)
		}
	}

	if err := bookOnce(depID, toCampus); err != nil {
		fmt.Println("❌ Booking failed:", err)
		os.Exit(1)
	}

	fmt.Println("✅ BUS BOOKED SUCCESSFULLY")
}

/* ================= TIME ================= */

func nextOccurrence(hhmm string) (time.Time, error) {
	loc, _ := time.LoadLocation("Africa/Casablanca")

	t, err := time.ParseInLocation("15:04", hhmm, loc)
	if err != nil {
		return time.Time{}, err
	}

	now := time.Now().In(loc)

	target := time.Date(
		now.Year(), now.Month(), now.Day(),
		t.Hour(), t.Minute(), 0, 0, loc,
	)

	if target.Before(now) {
		target = target.Add(24 * time.Hour)
	}
	return target, nil
}

func sleepUntil(t time.Time, label string) {
	for {
		now := time.Now()
		if !now.Before(t) {
			break
		}
		remaining := time.Until(t)

		if remaining > time.Second {
			fmt.Printf("⏳ %s in %v\r", label, remaining.Truncate(time.Second))
			time.Sleep(500 * time.Millisecond)
		} else {
			time.Sleep(5 * time.Millisecond)
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
	req, _ := http.NewRequest("GET", BASE_URL+"/departure/current", nil)
	applyHeaders(req)

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	// 🛑 Prevent HTML response
	if len(body) > 0 && body[0] == '<' {
		return 0, false, fmt.Errorf("HTML response (invalid token or blocked)")
	}

	var deps []Departure
	if err := json.Unmarshal(body, &deps); err != nil {
		return 0, false, err
	}

	var fallbackID int
	var fallbackToCampus bool

	for _, d := range deps {
		if d.Locked || d.Route.Name != ROUTE {
			continue
		}

		// Priority: TO_HOME
		if d.NbrToHome > 0 {
			fmt.Println("➡️ Found bus", d.ID, "TO_HOME")
			return d.ID, false, nil
		}

		// Fallback: TO_CAMPUS
		if d.NbrToCampus > 0 && fallbackID == 0 {
			fallbackID = d.ID
			fallbackToCampus = true
		}
	}

	if fallbackID != 0 {
		fmt.Println("➡️ Found bus", fallbackID, "TO_CAMPUS")
		return fallbackID, fallbackToCampus, nil
	}

	return 0, false, fmt.Errorf("no seats available")
}

/* ================= BOOKING ================= */

func bookOnce(depID int, toCampus bool) error {
	payload := BookingRequest{
		DepartureID: depID,
		ToCampus:    toCampus,
	}

	data, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST", BASE_URL+"/tickets/book", bytes.NewBuffer(data))
	applyHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusCreated {
		return nil
	}

	return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
}
