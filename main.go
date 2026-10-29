// Serves the static frontend plus one read-only price proxy. Standard library only.
// Build with ./build.sh: it fills web/, the only files the browser ever receives.
package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

//go:embed web
var webFiles embed.FS // frontend is compiled into the binary: nothing on disk to tamper with

var (
	yahooChartURL = "https://query1.finance.yahoo.com/v8/finance/chart/"
	validTicker   = regexp.MustCompile(`^[A-Z0-9.^=-]{1,12}$`)
	yahooClient   = &http.Client{Timeout: 10 * time.Second}
	yahooSlots    = make(chan struct{}, 8)
)

// handlePrices exists only because browsers can't call Yahoo directly (no CORS).
func handlePrices(w http.ResponseWriter, r *http.Request) {
	ticker := r.URL.Query().Get("ticker")
	years, err := strconv.ParseFloat(r.URL.Query().Get("years"), 64)
	if !validTicker.MatchString(ticker) || err != nil || !(years > 0 && years <= 30) {
		http.Error(w, "ticker: A-Z 0-9 . ^ = - (max 12); years: (0, 30]", http.StatusBadRequest)
		return
	}
	select {
	case yahooSlots <- struct{}{}:
		defer func() { <-yahooSlots }()
	default:
		http.Error(w, "busy, retry later", http.StatusServiceUnavailable)
		return
	}
	end := time.Now()
	start := end.Add(-time.Duration(years * 365 * 24 * float64(time.Hour)))
	u := yahooChartURL + url.PathEscape(ticker) + "?" + url.Values{"interval": {"1d"},
		"period1": {strconv.FormatInt(start.Unix(), 10)}, "period2": {strconv.FormatInt(end.Unix(), 10)}}.Encode()
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, u, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	var yahoo struct { // only these fields are decoded; everything else from upstream is dropped
		Chart struct {
			Result []struct {
				Meta struct {
					LongName, ShortName string
					GmtOffset           int64
				}
				Timestamp  []int64
				Indicators struct{ Quote []struct{ Close []*float64 } }
			}
		}
	}
	resp, err := yahooClient.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			err = json.NewDecoder(io.LimitReader(resp.Body, 5<<20)).Decode(&yahoo)
		} else {
			err = fmt.Errorf("upstream status %d", resp.StatusCode)
		}
	}
	if err != nil || len(yahoo.Chart.Result) == 0 || len(yahoo.Chart.Result[0].Indicators.Quote) == 0 {
		log.Printf("prices %s: %v", ticker, err) // details stay in the server log
		http.Error(w, "no market data for ticker", http.StatusBadGateway)
		return
	}
	result := yahoo.Chart.Result[0]
	prices := struct {
		Company string    `json:"company"`
		Dates   []string  `json:"dates"`
		Closes  []float64 `json:"closes"`
	}{result.Meta.LongName, []string{}, []float64{}}
	if prices.Company == "" {
		prices.Company = result.Meta.ShortName
	}
	for i, price := range result.Indicators.Quote[0].Close {
		if price != nil && i < len(result.Timestamp) { // exchange-local trading date, like yfinance
			date := time.Unix(result.Timestamp[i]+result.Meta.GmtOffset, 0).UTC().Format(time.DateOnly)
			prices.Dates, prices.Closes = append(prices.Dates, date), append(prices.Closes, *price)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(prices)
}

func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; "+
			"font-src https://cdn.jsdelivr.net/npm/@sap-theming/theming-base-content@11.36.5/; "+ // SAP 72, loaded by the browser
			"base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func main() {
	static, _ := fs.Sub(webFiles, "web")
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /api/prices", handlePrices)
	srv := &http.Server{Addr: "127.0.0.1:8080", Handler: withSecurityHeaders(mux), // localhost only: reached via reverse proxy
		ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 16 << 10}
	log.Fatal(srv.ListenAndServe())
}
