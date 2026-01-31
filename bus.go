package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const (
	BASE_URL       = "https://bus-med.1337.ma/api"
	TOKEN    = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOjIxOSwibG9naW4iOiJtb3phaG5vdSIsImlhdCI6MTc2OTY3NjE2MCwiZXhwIjoxNzcwMjgwOTYwfQ.qKF1BZ2_iXsxLdedwO_5FFEbjWVB9o45jTr_0CyHFQ4"
	PRELOAD_LEAD   = 10 * time.Second  // how many seconds before to prefetch departure
	REQUEST_TIMEOUT = 5 * time.Second  // per request timeout
)

type Departure struct {
	ID     int   `json:"id"`
	Locked bool  `json:"locked"`
	Route  Route `json:"route"`
}

type Route struct {
	Name string `json:"name"`
}

type BookingRequest struct {
	DepartureID int  `json:"departure_id"`
	ToCampus    bool `json:"to_campus"`
}

var httpClient = &http.Client{
	Timeout: REQUEST_TIMEOUT,
}

func main() {
	if len(os.Args) != 2 {
		fmt.Println("Usage: ./bus_reservation HH:MM")
		fmt.Println("Example: ./bus_reservation 19:30")
		os.Exit(1)
	}

	targetStr := os.Args[1]

	targetTime, err := nextOccurrence(targetStr)
	if err != nil {
		fmt.Printf("❌ Invalid time format: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("🎯 Target reservation time: %s\n", targetTime.Format(time.RFC3339))

	// Time to start preloading (few seconds before target)
	preloadTime := targetTime.Add(-PRELOAD_LEAD)
	now := time.Now()

	// Sleep until preload time
	if preloadTime.After(now) {
		fmt.Printf("⏳ Sleeping until preload: %s\n", preloadTime.Format(time.RFC3339))
		time.Sleep(time.Until(preloadTime))
	}

	fmt.Println("⚙️  Preloading departures & warming connection...")

	// Try to get departure ID early (may fail if not open yet, that's fine)
	departureID, err := getDepartureID()
	if err != nil {
		fmt.Printf("⚠️  Preload failed (will retry at target time): %v\n", err)
	} else if departureID == 0 {
		fmt.Println("⚠️  Preload: no available routes yet (will retry at target time).")
	} else {
		fmt.Printf("✅ Preload: got departure ID %d\n", departureID)
	}

	// Sleep until exact target time
	now = time.Now()
	if targetTime.After(now) {
		fmt.Printf("⏳ Waiting for exact target time: %s\n", targetTime.Format(time.RFC3339))
		time.Sleep(time.Until(targetTime))
	}

	fmt.Println("🚀 Time reached! Trying to book...")

	// Final attempt: if we don't have a departureID yet, fetch it again
	if departureID == 0 {
		departureID, err = getDepartureID()
		if err != nil {
			fmt.Printf("❌ Failed to get departure ID at booking time: %v\n", err)
			os.Exit(1)
		}
		if departureID == 0 {
			fmt.Printf("❌ No %s routes available at booking time\n", ROUTE)
			os.Exit(1)
		}
	}

	if err := bookTicket(departureID); err != nil {
		fmt.Printf("❌ Error booking bus: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("✅ Bus booked successfully!")
}

func nextOccurrence(hhmm string) (time.Time, error) {
	// Parse "HH:MM"
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return time.Time{}, err
	}
	now := time.Now()
	target := time.Date(
		now.Year(), now.Month(), now.Day(),
		t.Hour(), t.Minute(), 0, 0,
		now.Location(),
	)

	// If time already passed today, schedule for tomorrow
	if target.Before(now) {
		target = target.Add(24 * time.Hour)
	}
	return target, nil
}

func getDepartureID() (int, error) {
	req, err := http.NewRequest("GET", BASE_URL+"/departure/current", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Cookie", "le_token="+TOKEN)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("unexpected status from /departure/current: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var departures []Departure
	if err := json.Unmarshal(body, &departures); err != nil {
		return 0, err
	}

	for _, dep := range departures {
		if dep.Route.Name == ROUTE && !dep.Locked {
			return dep.ID, nil
		}
	}

	return 0, nil // no available departure for that route
}

func bookTicket(departureID int) error {
	booking := BookingRequest{
		DepartureID: departureID,
		ToCampus:    false,
	}

	bookingJSON, err := json.Marshal(booking)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", BASE_URL+"/tickets/book", bytes.NewBuffer(bookingJSON))
	if err != nil {
		return err
	}
	req.Header.Set("Cookie", "le_token="+TOKEN)
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
	return fmt.Errorf("booking failed, status: %d, body: %s", resp.StatusCode, string(body))
}
