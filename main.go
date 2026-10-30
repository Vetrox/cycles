// Serves the static frontend plus read-only Yahoo proxies for prices and ticker search. Standard library only.
// Build with ./build.sh: it fills web/, the only files the browser ever receives.
package main

import (
	"crypto/rand"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"stockcycles/guard"
)

//go:embed web
var webFiles embed.FS // frontend is compiled into the binary: nothing on disk to tamper with

// The /api handlers exist only because browsers can't call Yahoo directly (no CORS).
var (
	yahooChartURL  = "https://query1.finance.yahoo.com/v8/finance/chart/"
	yahooSearchURL = "https://query1.finance.yahoo.com/v1/finance/search"
	validTicker    = regexp.MustCompile(`^[A-Z0-9.^=-]{1,12}$`)
	validQuery     = regexp.MustCompile(`^[\p{L}\p{N} .,&'^=-]{1,40}$`)
	yahooClient    = &http.Client{Timeout: 10 * time.Second}
)

// getYahoo decodes only the fields v declares; everything else from upstream is dropped.
func getYahoo(r *http.Request, u string, maxBytes int64, v any) error {
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, u, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := yahooClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upstream status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxBytes)).Decode(v)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func handlePrices(w http.ResponseWriter, r *http.Request) {
	ticker := r.URL.Query().Get("ticker")
	years, err := strconv.ParseFloat(r.URL.Query().Get("years"), 64)
	if !validTicker.MatchString(ticker) || err != nil || !(years > 0 && years <= 30) {
		http.Error(w, "ticker: A-Z 0-9 . ^ = - (max 12); years: (0, 30]", http.StatusBadRequest)
		return
	}
	end := time.Now()
	start := end.Add(-time.Duration(years * 365 * 24 * float64(time.Hour)))
	u := yahooChartURL + url.PathEscape(ticker) + "?" + url.Values{"interval": {"1d"},
		"period1": {strconv.FormatInt(start.Unix(), 10)}, "period2": {strconv.FormatInt(end.Unix(), 10)}}.Encode()
	var yahoo struct {
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
	err = getYahoo(r, u, 5<<20, &yahoo)
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
	writeJSON(w, prices)
}

func handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if !validQuery.MatchString(q) {
		http.Error(w, "q: 1-40 letters, digits, spaces or . , & ' ^ = -", http.StatusBadRequest)
		return
	}
	type quote struct {
		Symbol    string `json:"symbol"`
		LongName  string `json:"longname"`
		ShortName string `json:"shortname"`
		ExchDisp  string `json:"exchDisp"`
		TypeDisp  string `json:"typeDisp"`
	}
	var yahoo struct{ Quotes []quote }
	u := yahooSearchURL + "?" + url.Values{"q": {q}, "quotesCount": {"8"}, "newsCount": {"0"}, "listsCount": {"0"}}.Encode()
	if err := getYahoo(r, u, 1<<20, &yahoo); err != nil {
		log.Printf("search %q: %v", q, err)
		http.Error(w, "search unavailable", http.StatusBadGateway)
		return
	}
	quotes := []quote{}
	for _, hit := range yahoo.Quotes {
		if validTicker.MatchString(hit.Symbol) { // only symbols /api/prices accepts
			quotes = append(quotes, hit)
		}
	}
	writeJSON(w, quotes)
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
	addr := flag.String("addr", "127.0.0.1:16950", "listen address, loopback only")
	flag.Parse()
	if host, _, err := net.SplitHostPort(*addr); err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		log.Fatalf("-addr %q: must be a loopback IP and port", *addr)
	}
	static, _ := fs.Sub(webFiles, "web")
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(static))
	key := make([]byte, 32)
	rand.Read(key) // new on every start: outstanding challenges just expire
	pow := guard.New(guard.Config{Key: key, PerMinute: 60, TTL: time.Minute, Tau: 10 * time.Second,
		MinBits: 19}) // ≈0.5 s in a desktop browser (TinyGo SHA-256, ~1 MH/s)
	mux.Handle("GET /api/challenge", pow.ChallengeHandler())
	mux.Handle("GET /api/prices", pow.Middleware(http.HandlerFunc(handlePrices)))
	mux.Handle("GET /api/search", pow.Middleware(http.HandlerFunc(handleSearch)))
	srv := &http.Server{Addr: *addr, Handler: withSecurityHeaders(mux),
		ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
		MaxHeaderBytes: 16 << 10}
	log.Fatal(srv.ListenAndServe())
}
