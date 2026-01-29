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

	PRELOAD_LEAD    = 10 * time.Second
	REQUEST_TIMEOUT = 3 * time.Second
	DIAL_TIMEOUT    = 2 * time.Second
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

	sleepUntil(target.Add(-PRELOAD_LEAD), "Preload")

	fmt.Println("⚙️  Fetching departure…")
	depID, toCampus, _ := getDeparture()

	sleepUntil(target, "Booking")

	fmt.Println("🚀 BOOKING")

	if depID == 0 {
		if depID, toCampus, err = getDeparture(); err != nil {
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

	for _, d := range deps {
		if d.Route.Name == ROUTE && !d.Locked {
			fmt.Printf("➡️ Found bus %d (%s)\n", d.ID, ROUTE)
			return d.ID, false, nil
		}
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
