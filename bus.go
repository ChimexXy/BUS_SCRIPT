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
	TOKEN = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOjIxOSwibG9naW4iOiJtb3phaG5vdSIsImlhdCI6MTc2OTM4OTEzOCwiZXhwIjoxNzY5OTkzOTM4fQ.S-k5fDk7ZqhZzKjbJEReMMzwPgkeG_IYUYXOcfUtWZg"

	ROUTE = "Martil" // or "Tetouan"

	PRELOAD_LEAD    = 10 * time.Second
	REQUEST_TIMEOUT = 5 * time.Second
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
	depID, toCampus, err := getDeparture()
	if err != nil {
		fmt.Println("⚠️ Preload failed:", err)
	}

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
	t, err := time.ParseInLocation("15:04", hhmm, time.Local)
	if err != nil {
		return time.Time{}, err
	}

	now := time.Now()
	target := time.Date(
		now.Year(), now.Month(), now.Day(),
		t.Hour(), t.Minute(), 0, 0, time.Local,
	)

	if target.Before(now) {
		target = target.Add(24 * time.Hour)
	}
	return target, nil
}

func sleepUntil(t time.Time, label string) {
	for time.Now().Before(t) {
		fmt.Printf("⏳ %s in %v\r", label, time.Until(t).Truncate(time.Second))
		time.Sleep(1 * time.Second)
	}
	fmt.Print("\n")
}

/* ================= API ================= */

func getDeparture() (int, bool, error) {
	req, _ := http.NewRequest("GET", BASE_URL+"/departure/current", nil)
	req.Header.Set("Cookie", "le_token="+TOKEN)

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var deps []Departure
	if err := json.Unmarshal(body, &deps); err != nil {
		return 0, false, err
	}

	for _, d := range deps {
		if d.Locked || d.Route.Name != ROUTE {
			continue
		}

		if d.NbrToHome > 0 {
			fmt.Println("➡️ Direction: TO_HOME")
			return d.ID, false, nil
		}
		if d.NbrToCampus > 0 {
			fmt.Println("➡️ Direction: TO_CAMPUS")
			return d.ID, true, nil
		}
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
	req.Header.Set("Cookie", "le_token="+TOKEN)
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
