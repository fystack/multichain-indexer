package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/fystack/multichain-indexer/internal/worker"
)

// Operator endpoints on the health server, enabled only when INDEXER_ADMIN_TOKEN is set; callers send it as a
// bearer token. {network} is a chain's network_id or its config key.
//
//	PUT    /admin/chains/{network}/nodes   {"urls": ["https://..."]}  replace the RPC nodes now, kept across restarts
//	DELETE /admin/chains/{network}/nodes                              back to the nodes in config
//	POST   /admin/chains/{network}/rescan  {"from": 1, "to": 2}       queue a block range for the manual worker
func registerAdminRoutes(mux *http.ServeMux, manager *worker.Manager, token string) {
	authorized := func(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			given := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
				writeError(w, http.StatusUnauthorized, "missing or wrong admin token")
				return
			}
			chain, ok := manager.ChainForNetwork(r.PathValue("network"))
			if !ok {
				writeError(w, http.StatusNotFound, "no such chain")
				return
			}
			next(w, r, chain)
		}
	}

	mux.HandleFunc("PUT /admin/chains/{network}/nodes", authorized(func(w http.ResponseWriter, r *http.Request, chain string) {
		var body struct {
			URLs []string `json:"urls"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "body must be {\"urls\": [...]}")
			return
		}
		respond(w, manager.SetChainNodes(chain, body.URLs))
	}))

	mux.HandleFunc("DELETE /admin/chains/{network}/nodes", authorized(func(w http.ResponseWriter, r *http.Request, chain string) {
		respond(w, manager.ResetChainNodes(chain))
	}))

	mux.HandleFunc("POST /admin/chains/{network}/rescan", authorized(func(w http.ResponseWriter, r *http.Request, chain string) {
		var body struct {
			From uint64 `json:"from"`
			To   uint64 `json:"to"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "body must be {\"from\": n, \"to\": m}")
			return
		}
		respond(w, manager.QueueRescan(r.Context(), chain, body.From, body.To))
	}))
}

func respond(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, worker.ErrUnknownChain):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, worker.ErrNodeControlMissing):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
