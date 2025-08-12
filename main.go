// package main

// import (
// 	"crypto/hmac"
// 	"crypto/sha256"
// 	"encoding/binary"
// 	"encoding/hex"
// 	"encoding/json"
// 	"errors"
// 	"fmt"
// 	"log"
// 	"math"
// 	"net/http"
// 	"sort"
// 	"strconv"
// 	"strings"
// 	"sync"
// 	"time"
// )

// const (
// 	boardSize   = 40 // numbers 1..40
// 	drawCount   = 10 // draw 10 numbers
// 	floatsNeed  = drawCount
// 	bytesNeeded = floatsNeed * 4 // 40 bytes
// )

// type RiskLevel string

// const (
// 	RiskClassic RiskLevel = "CLASSIC"
// 	RiskLow     RiskLevel = "LOW"
// 	RiskMedium  RiskLevel = "MEDIUM"
// 	RiskHigh    RiskLevel = "HIGH"
// )

// type ServerState struct {
// 	mu sync.Mutex

// 	// fairness/rotation
// 	serverSeed     []byte // 32 bytes
// 	serverSeedHash string // hex SHA-256
// 	rotationID     int64
// 	nonce          uint64 // increments per bet

// 	// one global client seed for demo (multi-user -> map[userID]clientSeed)
// 	clientSeed string

// 	// track nonce window per rotation
// 	nonceStart uint64
// 	nonceEnd   uint64
// }

// var S = &ServerState{}

// func main() {
// 	// init rotation
// 	S.rotateSeedLocked("demo-initial-client-seed")

// 	mux := http.NewServeMux()
// 	mux.HandleFunc("/fairness/current", handleFairnessCurrent)
// 	mux.HandleFunc("/fairness/client-seed", handleSetClientSeed)
// 	mux.HandleFunc("/fairness/rotate", handleRotate) // demo endpoint to rotate seed
// 	mux.HandleFunc("/fairness/reveal", handleReveal)

// 	mux.HandleFunc("/keno/bet", handleKenoBet)

// 	addr := ":8080"
// 	log.Printf("Keno demo API listening on %s", addr)
// 	log.Fatal(http.ListenAndServe(addr, logRequest(mux)))
// }

// func logRequest(h http.Handler) http.Handler {
// 	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
// 		start := time.Now()
// 		h.ServeHTTP(w, r)
// 		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
// 	})
// }

// func writeJSON(w http.ResponseWriter, status int, v any) {
// 	w.Header().Set("Content-Type", "application/json")
// 	w.WriteHeader(status)
// 	_ = json.NewEncoder(w).Encode(v)
// }

// func badRequest(w http.ResponseWriter, msg string) {
// 	writeJSON(w, http.StatusBadRequest, map[string]any{"error": msg})
// }

// /* -------------------- FAIRNESS ENDPOINTS -------------------- */

// func handleFairnessCurrent(w http.ResponseWriter, r *http.Request) {
// 	if r.Method != http.MethodGet {
// 		badRequest(w, "GET only")
// 		return
// 	}
// 	S.mu.Lock()
// 	defer S.mu.Unlock()
// 	writeJSON(w, http.StatusOK, map[string]any{
// 		"rotationId":     S.rotationID,
// 		"serverSeedHash": S.serverSeedHash,
// 		"clientSeed":     S.clientSeed,
// 		"nonce":          S.nonce,
// 	})
// }

// // change client seed
// type setClientSeedReq struct {
// 	ClientSeed string `json:"clientSeed"`
// }

// func handleSetClientSeed(w http.ResponseWriter, r *http.Request) {
// 	if r.Method != http.MethodPost {
// 		badRequest(w, "POST only")
// 		return
// 	}
// 	var req setClientSeedReq
// 	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.ClientSeed) == "" {
// 		badRequest(w, "invalid clientSeed")
// 		return
// 	}
// 	S.mu.Lock()
// 	S.clientSeed = strings.TrimSpace(req.ClientSeed)
// 	cs := S.clientSeed
// 	S.mu.Unlock()
// 	writeJSON(w, http.StatusOK, map[string]string{"clientSeed": cs})
// }

// // demo: rotate seed (in real life: schedule/ops)
// func handleRotate(w http.ResponseWriter, r *http.Request) {
// 	if r.Method != http.MethodPost {
// 		badRequest(w, "POST only")
// 		return
// 	}
// 	S.mu.Lock()
// 	defer S.mu.Unlock()
// 	S.rotateSeedLocked(S.clientSeed)
// 	writeJSON(w, http.StatusOK, map[string]any{
// 		"rotationId":     S.rotationID,
// 		"serverSeedHash": S.serverSeedHash,
// 		"nonceStart":     S.nonceStart,
// 		"nonceEnd":       S.nonceEnd,
// 	})
// }

// func handleReveal(w http.ResponseWriter, r *http.Request) {
// 	// reveal current rotation (demo). Real-world: reveal previous rotations after switch.
// 	if r.Method != http.MethodGet {
// 		badRequest(w, "GET only")
// 		return
// 	}
// 	S.mu.Lock()
// 	defer S.mu.Unlock()
// 	writeJSON(w, http.StatusOK, map[string]any{
// 		"rotationId": S.rotationID,
// 		"serverSeed": hex.EncodeToString(S.serverSeed),
// 		"nonceStart": S.nonceStart,
// 		"nonceEnd":   S.nonceEnd,
// 	})
// }

// func (s *ServerState) rotateSeedLocked(keepClientSeed string) {
// 	// generate new seed (demo: derive from time; prod: crypto/rand 32 bytes)
// 	now := time.Now().UnixNano()
// 	raw := sha256.Sum256([]byte(fmt.Sprintf("demo-seed-%d", now)))
// 	s.serverSeed = raw[:]
// 	h := sha256.Sum256(s.serverSeed)
// 	s.serverSeedHash = hex.EncodeToString(h[:])

// 	// rotation bookkeeping
// 	s.rotationID++
// 	s.nonceStart = s.nonce
// 	s.nonceEnd = s.nonce // will advance as bets happen

// 	if keepClientSeed != "" {
// 		s.clientSeed = keepClientSeed
// 	}
// }

// /* -------------------- BET ENDPOINT -------------------- */

// type betReq struct {
// 	Amount     float64 `json:"amount"`
// 	Numbers    []int   `json:"numbers"`    // 1..40, len 1..10
// 	RiskLevel  string  `json:"riskLevel"`  // CLASSIC|LOW|MEDIUM|HIGH
// 	ClientSeed *string `json:"clientSeed"` // optional: only if changed
// }

// type betResp struct {
// 	Draw       []int   `json:"draw"`
// 	Matches    int     `json:"matches"`
// 	Payout     float64 `json:"payout"`
// 	RiskLevel  string  `json:"riskLevel"`
// 	NonceUsed  uint64  `json:"nonceUsed"`
// 	RotationID int64   `json:"rotationId"`
// }

// func handleKenoBet(w http.ResponseWriter, r *http.Request) {
// 	if r.Method != http.MethodPost {
// 		badRequest(w, "POST only")
// 		return
// 	}
// 	var req betReq
// 	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
// 		badRequest(w, "invalid json")
// 		return
// 	}
// 	// validate
// 	if req.Amount <= 0 {
// 		badRequest(w, "amount must be > 0")
// 		return
// 	}
// 	if len(req.Numbers) < 1 || len(req.Numbers) > 10 {
// 		badRequest(w, "you must pick 1..10 numbers")
// 		return
// 	}
// 	if !validRisk(req.RiskLevel) {
// 		badRequest(w, "invalid riskLevel")
// 		return
// 	}
// 	if !validNumbers(req.Numbers) {
// 		badRequest(w, "numbers must be unique in 1..40")
// 		return
// 	}

// 	S.mu.Lock()
// 	// update clientSeed for this bet if changed
// 	if req.ClientSeed != nil && strings.TrimSpace(*req.ClientSeed) != "" {
// 		S.clientSeed = strings.TrimSpace(*req.ClientSeed)
// 	}
// 	nonceUsed := S.nonce
// 	clientSeed := S.clientSeed
// 	serverSeed := append([]byte(nil), S.serverSeed...) // copy
// 	rotationID := S.rotationID
// 	S.nonce++ // increment per bet
// 	S.nonceEnd = S.nonce
// 	S.mu.Unlock()

// 	// provably-fair draw 10 numbers
// 	draw, err := draw10(serverSeed, clientSeed, nonceUsed)
// 	if err != nil {
// 		badRequest(w, "draw failed: "+err.Error())
// 		return
// 	}

// 	// matches
// 	m := countMatches(req.Numbers, draw)

// 	// payout (DEMO ONLY: illustrative multipliers; not RTP balanced)
// 	k := len(req.Numbers)
// 	risk := RiskLevel(strings.ToUpper(req.RiskLevel))
// 	multi := multiplierDemo(risk, k, m) // returns 0..something
// 	payout := round2(req.Amount * multi)

// 	resp := betResp{
// 		Draw:       draw,
// 		Matches:    m,
// 		Payout:     payout,
// 		RiskLevel:  string(risk),
// 		NonceUsed:  nonceUsed,
// 		RotationID: rotationID,
// 	}
// 	writeJSON(w, http.StatusOK, resp)
// }

// func validRisk(s string) bool {
// 	switch strings.ToUpper(s) {
// 	case "CLASSIC", "LOW", "MEDIUM", "HIGH":
// 		return true
// 	default:
// 		return false
// 	}
// }

// func validNumbers(nums []int) bool {
// 	if len(nums) == 0 || len(nums) > 10 {
// 		return false
// 	}
// 	seen := map[int]bool{}
// 	for _, n := range nums {
// 		if n < 1 || n > boardSize || seen[n] {
// 			return false
// 		}
// 		seen[n] = true
// 	}
// 	return true
// }

// func countMatches(picks, draw []int) int {
// 	set := map[int]bool{}
// 	for _, d := range draw {
// 		set[d] = true
// 	}
// 	m := 0
// 	for _, p := range picks {
// 		if set[p] {
// 			m++
// 		}
// 	}
// 	return m
// }

// /* -------------------- PROVABLY FAIR CORE -------------------- */

// // HMAC paging: message = clientSeed : nonce : cursor   (ASCII, no padding)
// func hmacPage(serverSeed []byte, clientSeed string, nonce uint64, cursor uint32) [32]byte {
// 	msg := clientSeed + ":" + strconv.FormatUint(nonce, 10) + ":" + strconv.FormatUint(uint64(cursor), 10)
// 	m := hmac.New(sha256.New, serverSeed)
// 	m.Write([]byte(msg))
// 	var out [32]byte
// 	copy(out[:], m.Sum(nil))
// 	return out
// }

// // generate at least `need` bytes by concatenating pages cursor=0,1,2,...
// func byteStream(serverSeed []byte, clientSeed string, nonce uint64, need int) []byte {
// 	out := make([]byte, 0, need)
// 	var cursor uint32 = 0
// 	for len(out) < need {
// 		page := hmacPage(serverSeed, clientSeed, nonce, cursor)
// 		rem := need - len(out)
// 		if rem >= 32 {
// 			out = append(out, page[:]...)
// 		} else {
// 			out = append(out, page[:rem]...)
// 		}
// 		cursor++
// 	}
// 	return out
// }

// // bytes -> floats in [0,1), big-endian u32 / 2^32
// func bytesToFloats(b []byte, count int) ([]float64, error) {
// 	if len(b) < count*4 {
// 		return nil, errors.New("insufficient bytes")
// 	}
// 	floats := make([]float64, count)
// 	for i := 0; i < count; i++ {
// 		u := binary.BigEndian.Uint32(b[i*4 : i*4+4])
// 		floats[i] = float64(u) / 4294967296.0 // 2^32
// 	}
// 	return floats, nil
// }

// // floats -> 10 unique hits 1..40 (Fisher–Yates shrink)
// func floatsToHits(fs []float64) []int {
// 	S := make([]int, boardSize)
// 	for i := 0; i < boardSize; i++ {
// 		S[i] = i + 1
// 	}
// 	hits := make([]int, 0, drawCount)
// 	for i := 0; i < drawCount; i++ {
// 		size := boardSize - i
// 		idx := int(math.Floor(fs[i] * float64(size)))
// 		if idx >= size { // paranoia guard (shouldn't happen)
// 			idx = size - 1
// 		}
// 		hits = append(hits, S[idx])
// 		S[idx] = S[size-1]
// 		S = S[:size-1]
// 	}
// 	sort.Ints(hits) // sort for nicer display (optional)
// 	return hits
// }

// func draw10(serverSeed []byte, clientSeed string, nonce uint64) ([]int, error) {
// 	bytes := byteStream(serverSeed, clientSeed, nonce, bytesNeeded) // 40 bytes
// 	fs, err := bytesToFloats(bytes, floatsNeed)
// 	if err != nil {
// 		return nil, err
// 	}
// 	return floatsToHits(fs), nil
// }

// /* -------------------- DEMO PAYOUT (not balanced) -------------------- */

// // multiplierDemo: simple illustrative table generator by (risk, k, hits).
// // NOTE: This is ONLY for demo API wiring — not RTP-balanced.
// // Feel free to replace by a real table loaded from config.
// func multiplierDemo(r RiskLevel, k, h int) float64 {
// 	if h == 0 {
// 		return 0
// 	}
// 	// base grows ~ cubically by hits, clipped by k
// 	base := math.Pow(float64(h), 3) / math.Pow(float64(k), 0.5) // soft growth
// 	// classic baseline shaping
// 	classic := map[int]float64{
// 		1:  3, // 1/1
// 		2:  5,
// 		3:  12,
// 		4:  40,
// 		5:  120,
// 		6:  240,
// 		7:  500,
// 		8:  900,
// 		9:  1500,
// 		10: 2500,
// 	}
// 	boost := classic[h]
// 	if boost == 0 {
// 		// if hits>10 (impossible) or >k but <=10, just cap
// 		boost = classic[min(h, 10)]
// 	}
// 	// combine
// 	m := (base * 0.35) + (boost * 0.65)

// 	// risk shaping (variance only; pretend same RTP on average)
// 	switch r {
// 	case RiskLow:
// 		// pay more on mid hits, less on top hits
// 		if h <= k/2 {
// 			m *= 0.9
// 		} else if h < k {
// 			m *= 0.85
// 		} else { // max hit
// 			m *= 0.7
// 		}
// 		m += float64(h) * 0.2 // small floor for frequent mini-returns
// 	case RiskMedium:
// 		// near classic
// 		m *= 1.0
// 	case RiskHigh:
// 		// nerf low hits, buff top hits
// 		if h <= k/2 {
// 			m *= 0.6
// 		} else if h < k {
// 			m *= 1.15
// 		} else { // max hit
// 			m *= 1.5
// 		}
// 	case RiskClassic:
// 		// baseline
// 	}
// 	// make sure hitting less than half often pays tiny or zero
// 	if h < (k+1)/2 {
// 		m *= 0.5
// 	}
// 	// floor small values to neat steps
// 	return round2(max(0, m))
// }

// /* -------------------- utils -------------------- */

// func round2(v float64) float64 {
// 	return math.Round(v*100) / 100
// }
// func max(a, b float64) float64 {
// 	if a > b {
// 		return a
// 	}
// 	return b
// }
// func min(a, b int) int {
// 	if a < b {
// 		return a
// 	}
// 	return b
// }

package main

import (
	"fmt"
	"math"
)

const (
	N = 40 // tiles tổng
	D = 10 // rút 10 số
)

// logC(n,k) dùng lgamma để tránh overflow
func logC(n, k int) float64 {
	if k < 0 || k > n {
		return math.Inf(-1)
	}
	// C(n,k) = exp(lgamma(n+1) - lgamma(k+1) - lgamma(n-k+1))
	a, _ := math.Lgamma(float64(n + 1))
	b, _ := math.Lgamma(float64(k + 1))
	c, _ := math.Lgamma(float64(n - k + 1))
	return a - b - c
}

// P(h;k): hypergeometric
func prob(k, h int) float64 {
	if h < 0 || h > k || h > D || D-h > (N-k) {
		return 0
	}
	lnum := logC(k, h) + logC(N-k, D-h)
	lden := logC(N, D)
	return math.Exp(lnum - lden)
}

// shape weights cho từng risk (ví dụ minh hoạ)
// trả về mảng w[h] cho h=0..k
func weights(risk string, k int) []float64 {
	w := make([]float64, k+1)
	switch risk {
	case "LOW":
		for h := 0; h <= k; h++ {
			// ưu tiên h thấp/vừa; h=0 vẫn có thể 0
			w[h] = math.Pow(float64(h), 1.2) + 0.4*float64(h)
		}
	case "HIGH":
		for h := 0; h <= k; h++ {
			// dồn vào h cao, h nhỏ ~0
			w[h] = math.Pow(float64(h), 3.0)
			if h < (k+1)/2 {
				w[h] *= 0.1
			} // nerf h thấp
		}
	case "MEDIUM":
		for h := 0; h <= k; h++ {
			w[h] = math.Pow(float64(h), 2.0) + 0.2*float64(h)
		}
	default: // "CLASSIC"
		for h := 0; h <= k; h++ {
			// shape “chuẩn” tăng theo h nhưng không quá gắt
			w[h] = math.Pow(float64(h), 1.7) + 0.3*float64(h)
		}
	}
	// Không trả cho h=0 nếu muốn (thường =0)
	w[0] = 0
	return w
}

// build payout multipliers cho (risk,k) với RTP target
func buildPayout(risk string, k int, rtpTarget float64) []float64 {
	w := weights(risk, k)
	// EV_raw = sum P(h;k)*w[h]
	evRaw := 0.0
	for h := 0; h <= k; h++ {
		evRaw += prob(k, h) * w[h]
	}
	if evRaw == 0 {
		return make([]float64, k+1)
	}
	s := rtpTarget / evRaw

	// scale + làm tròn đẹp
	m := make([]float64, k+1)
	for h := 0; h <= k; h++ {
		val := s * w[h]
		// làm tròn bậc thang 0.25x
		val = math.Round(val*4) / 4.0
		// tuỳ luật hiển thị: nếu muốn “payout bao gồm vốn” thì giữ nguyên;
		// nếu muốn “chỉ lợi nhuận”, trừ 1 ở các h>0 rồi clamp 0.
		m[h] = math.Max(0, val)
	}
	return m
}

func main() {
	rtp := 0.99 // ví dụ Classic 99%
	for _, risk := range []string{"CLASSIC", "LOW", "MEDIUM", "HIGH"} {
		fmt.Println("====", risk, "====")
		for k := 1; k <= 10; k++ {
			m := buildPayout(risk, k, rtp)
			// kiểm tra RTP thực tế sau khi làm tròn
			ev := 0.0
			for h := 0; h <= k; h++ {
				ev += prob(k, h) * m[h]
			}
			fmt.Printf("k=%d RTP=%.4f  multipliers:", k, ev)
			for h := 0; h <= k; h++ {
				fmt.Printf(" h=%d:%.2f", h, m[h])
			}
			fmt.Println()
		}
	}
}
