package main

import (
	"bytes"
	"context"
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

	ROUTE = "Martil" // or "Tetouan"

	PRELOAD_LEAD    = 10 * time.Second
	REQUEST_TIMEOUT = 8 * time.Second
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

/* ================= GLOBAL TOKEN ================= */

var TOKEN string

func init() {
	TOKEN = os.Getenv("BUS_TOKEN")
	if TOKEN == "" {
		fmt.Println("❌ BUS_TOKEN environment variable not set")
		os.Exit(1)
	}

	// Force Go DNS resolver (CRITICAL for Android)
	os.Setenv("GODEBUG", "netdns=go")
}

/* ================= HTTP CLIENT ================= */

var httpClient = &http.Client{
	Timeout: REQUEST_TIMEOUT,
	Transport: &http.Transport{
		ForceAttemptHTTP2: false,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := net.Dialer{
				Timeout: 5 * time.Second,
				Resolver: &net.Resolver{
					PreferGo: true,
				},
			}
			return d.DialContext(ctx, "tcp4", addr) // FORCE IPv4
		},
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
	loc, err := time.LoadLocation("Africa/Casablanca")
	if err != nil {
		return time.Time{}, err
	}

	t, err := time.ParseInLocation("15:04", hhmm, loc)
	if err != nil {
		return time.Time{}, err
	}

	now := time.Now().In(loc)

	target := time.Date(
		now.
