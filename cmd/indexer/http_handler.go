package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/fystack/multichain-indexer/internal/worker"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
)

type HealthResponse struct {
	Status    string    `json:"status"`
	Timestamp time.Time `json:"timestamp"`
	Version   string    `json:"version"`
}

type APIErrorResponse struct {
	Status    string    `json:"status"`
	Error     string    `json:"error"`
	Timestamp time.Time `json:"timestamp"`
}

type TonWalletReloadResponse struct {
	Status           string                         `json:"status"`
	Source           worker.WalletReloadSource      `json:"source"`
	Chain            string                         `json:"chain,omitempty"`
	Results          []worker.TonWalletReloadResult `json:"results"`
	TriggeredAtUTC   time.Time                      `json:"triggered_at_utc"`
	SupportedSources []worker.WalletReloadSource    `json:"supported_sources"`
}

type TonJettonReloadResponse struct {
	Status         string                         `json:"status"`
	Chain          string                         `json:"chain,omitempty"`
	Results        []worker.TonJettonReloadResult `json:"results"`
	TriggeredAtUTC time.Time                      `json:"triggered_at_utc"`
}

type IndexerHTTPHandler struct {
	version                string
	tonWalletReloadService *worker.TonWalletReloadService
	tonJettonReloadService *worker.TonJettonReloadService
}

func NewIndexerHTTPHandler(
	version string,
	tonWalletReloadService *worker.TonWalletReloadService,
	tonJettonReloadService *worker.TonJettonReloadService,
) *IndexerHTTPHandler {
	return &IndexerHTTPHandler{
		version:                version,
		tonWalletReloadService: tonWalletReloadService,
		tonJettonReloadService: tonJettonReloadService,
	}
}

func (h *IndexerHTTPHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/health", h.HandleHealth)
	mux.HandleFunc("/ton/wallets/reload", h.HandleTonWalletReload)
	mux.HandleFunc("/ton/jettons/reload", h.HandleTonJettonReload)
}

func (h *IndexerHTTPHandler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	response := HealthResponse{
		Status:    "ok",
		Timestamp: time.Now().UTC(),
		Version:   h.version,
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *IndexerHTTPHandler) HandleTonWalletReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	req, err := h.parseTonWalletReloadRequest(r)
	if err != nil {
		writeErrorJSON(w, http.StatusBadRequest, err.Error())
		return
	}

	results, err := h.tonWalletReloadService.ReloadTonWallets(r.Context(), req)
	if err != nil {
		statusCode := http.StatusInternalServerError
		if errors.Is(err, worker.ErrTonWorkerNotFound) || errors.Is(err, worker.ErrNoTonWorkerConfigured) {
			statusCode = http.StatusNotFound
		}
		writeErrorJSON(w, statusCode, err.Error())
		return
	}

	response := TonWalletReloadResponse{
		Status:           reloadWalletStatus(results),
		Source:           req.Source.Normalize(),
		Chain:            req.ChainFilter,
		Results:          results,
		TriggeredAtUTC:   time.Now().UTC(),
		SupportedSources: []worker.WalletReloadSource{worker.WalletReloadSourceKV, worker.WalletReloadSourceDB},
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *IndexerHTTPHandler) parseTonWalletReloadRequest(r *http.Request) (worker.TonWalletReloadRequest, error) {
	source := worker.WalletReloadSource(strings.TrimSpace(r.URL.Query().Get("source")))
	if source == "" {
		source = worker.WalletReloadSourceKV
	}
	if !source.IsValid() {
		return worker.TonWalletReloadRequest{}, fmt.Errorf("invalid source (supported: kv, db)")
	}

	return worker.TonWalletReloadRequest{
		Source:      source,
		ChainFilter: strings.TrimSpace(r.URL.Query().Get("chain")),
	}, nil
}

func (h *IndexerHTTPHandler) HandleTonJettonReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErrorJSON(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if h.tonJettonReloadService == nil {
		writeErrorJSON(w, http.StatusNotFound, worker.ErrNoTonWorkerConfigured.Error())
		return
	}

	req := worker.TonJettonReloadRequest{
		ChainFilter: strings.TrimSpace(r.URL.Query().Get("chain")),
	}

	results, err := h.tonJettonReloadService.ReloadTonJettons(r.Context(), req)
	if err != nil {
		statusCode := http.StatusInternalServerError
		if errors.Is(err, worker.ErrTonWorkerNotFound) || errors.Is(err, worker.ErrNoTonWorkerConfigured) {
			statusCode = http.StatusNotFound
		}
		writeErrorJSON(w, statusCode, err.Error())
		return
	}

	response := TonJettonReloadResponse{
		Status:         reloadJettonStatus(results),
		Chain:          req.ChainFilter,
		Results:        results,
		TriggeredAtUTC: time.Now().UTC(),
	}
	writeJSON(w, http.StatusOK, response)
}

func reloadWalletStatus(results []worker.TonWalletReloadResult) string {
	for _, result := range results {
		if result.Error != "" {
			return "partial_error"
		}
	}
	return "ok"
}

func reloadJettonStatus(results []worker.TonJettonReloadResult) string {
	for _, result := range results {
		if result.Error != "" {
			return "partial_error"
		}
	}
	return "ok"
}

func startHTTPServer(
	port int,
	cfg *config.Config,
	tonWalletReloadService *worker.TonWalletReloadService,
	tonJettonReloadService *worker.TonJettonReloadService,
) *http.Server {
	mux := http.NewServeMux()

	version := cfg.Version
	if version == "" {
		version = "1.0.0" // fallback version
	}

	handler := NewIndexerHTTPHandler(version, tonWalletReloadService, tonJettonReloadService)
	handler.Register(mux)

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	go func() {
		logger.Info(
			"Indexer HTTP server started",
			"port", port,
			"health_endpoint", "/health",
			"wallet_reload_endpoint", "/ton/wallets/reload",
			"jetton_reload_endpoint", "/ton/jettons/reload",
		)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server failed to start", "error", err)
		}
	}()

	return server
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		logger.Error("Failed to encode response", "status", statusCode, "err", err)
	}
}

func writeErrorJSON(w http.ResponseWriter, statusCode int, message string) {
	writeJSON(w, statusCode, APIErrorResponse{
		Status:    "error",
		Error:     message,
		Timestamp: time.Now().UTC(),
	})
}
