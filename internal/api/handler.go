package api

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/fystack/multichain-indexer/internal/worker"
	"github.com/fystack/multichain-indexer/pkg/store/missingblockstore"
)

type Handler struct {
	version                string
	missingBlocksStore     missingblockstore.MissingBlocksStore
	walletReloadService    *worker.WalletReloadService
	tonJettonReloadService *worker.TonJettonReloadService
}

type healthResponse struct {
	Status    string    `json:"status"`
	Timestamp time.Time `json:"timestamp"`
	Version   string    `json:"version"`
}

type rescanRequest struct {
	NetworkCode string `json:"network_code" binding:"required"`
	From        uint64 `json:"from" binding:"required,min=1"`
	To          uint64 `json:"to" binding:"required,min=1,gtefield=From"`
}

type rescanResponse struct {
	Message     string `json:"message"`
	NetworkCode string `json:"network_code"`
	From        uint64 `json:"from"`
	To          uint64 `json:"to"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type walletReloadRequest struct {
	Source string `json:"source"`
	Type   string `json:"type" binding:"required"`
	Chain  string `json:"chain"`
}

type walletReloadResponse struct {
	Status           string                      `json:"status"`
	Source           worker.WalletReloadSource   `json:"source"`
	Type             string                      `json:"type"`
	Chain            string                      `json:"chain,omitempty"`
	Results          []worker.WalletReloadResult `json:"results"`
	TriggeredAtUTC   time.Time                   `json:"triggered_at_utc"`
	SupportedSources []worker.WalletReloadSource `json:"supported_sources"`
}

type tonJettonReloadRequest struct {
	Chain string `json:"chain"`
}

type tonJettonReloadResponse struct {
	Status         string                         `json:"status"`
	Chain          string                         `json:"chain,omitempty"`
	Results        []worker.TonJettonReloadResult `json:"results"`
	TriggeredAtUTC time.Time                      `json:"triggered_at_utc"`
}

func NewHandler(
	version string,
	missingBlocksStore missingblockstore.MissingBlocksStore,
	walletReloadService *worker.WalletReloadService,
	tonJettonReloadService *worker.TonJettonReloadService,
) *Handler {
	return &Handler{
		version:                version,
		missingBlocksStore:     missingBlocksStore,
		walletReloadService:    walletReloadService,
		tonJettonReloadService: tonJettonReloadService,
	}
}

func (h *Handler) Router() *gin.Engine {
	router := gin.New()
	router.Use(gin.Recovery())

	router.GET("/health", h.handleHealth)

	v1 := router.Group("/api/v1")
	v1.POST("/networks/rescan", h.handleRescan)
	v1.POST("/wallets/reload", h.handleWalletReload)
	v1.POST("/ton/jettons/reload", h.handleTonJettonReload)

	return router
}

func (h *Handler) handleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, healthResponse{
		Status:    "ok",
		Timestamp: time.Now().UTC(),
		Version:   h.version,
	})
}

func (h *Handler) handleRescan(c *gin.Context) {
	var req rescanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	if err := h.missingBlocksStore.AddMissingBlockRange(c.Request.Context(), req.NetworkCode, req.From, req.To); err != nil {
		c.JSON(http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, rescanResponse{
		Message:     "block range added",
		NetworkCode: req.NetworkCode,
		From:        req.From,
		To:          req.To,
	})
}

func (h *Handler) handleWalletReload(c *gin.Context) {
	var req walletReloadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	source := worker.WalletReloadSource(req.Source)
	if !source.IsValid() {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid source"})
		return
	}
	if source == "" {
		source = worker.WalletReloadSourceDB
	}

	networkType := worker.ParseNetworkType(req.Type)
	if networkType == "" {
		c.JSON(http.StatusBadRequest, errorResponse{Error: "invalid type"})
		return
	}

	results, err := h.walletReloadService.ReloadWallets(c.Request.Context(), worker.WalletReloadRequest{
		Source:      source,
		TypeFilter:  networkType,
		ChainFilter: req.Chain,
	})
	if err != nil {
		statusCode := http.StatusInternalServerError
		if errors.Is(err, worker.ErrNoTonWorkerConfigured) || errors.Is(err, worker.ErrTonWorkerNotFound) {
			statusCode = http.StatusNotFound
		}
		c.JSON(statusCode, errorResponse{Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, walletReloadResponse{
		Status:           reloadWalletStatus(results),
		Source:           source.Normalize(),
		Type:             string(networkType),
		Chain:            req.Chain,
		Results:          results,
		TriggeredAtUTC:   time.Now().UTC(),
		SupportedSources: []worker.WalletReloadSource{worker.WalletReloadSourceDB},
	})
}

func (h *Handler) handleTonJettonReload(c *gin.Context) {
	var req tonJettonReloadRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}

	results, err := h.tonJettonReloadService.ReloadTonJettons(c.Request.Context(), worker.TonJettonReloadRequest{
		ChainFilter: req.Chain,
	})
	if err != nil {
		statusCode := http.StatusInternalServerError
		if errors.Is(err, worker.ErrNoTonWorkerConfigured) || errors.Is(err, worker.ErrTonWorkerNotFound) {
			statusCode = http.StatusNotFound
		}
		c.JSON(statusCode, errorResponse{Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, tonJettonReloadResponse{
		Status:         reloadTonJettonStatus(results),
		Chain:          req.Chain,
		Results:        results,
		TriggeredAtUTC: time.Now().UTC(),
	})
}

func reloadWalletStatus(results []worker.WalletReloadResult) string {
	for _, result := range results {
		if result.Error != "" {
			return "partial_error"
		}
	}
	return "ok"
}

func reloadTonJettonStatus(results []worker.TonJettonReloadResult) string {
	for _, result := range results {
		if result.Error != "" {
			return "partial_error"
		}
	}
	return "ok"
}
