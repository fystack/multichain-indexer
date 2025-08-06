package pool

import (
	"log/slog"
	"sync"
	"time"
)

type Pool struct {
	nodes         []string
	currentIdx    int
	failedNodes   map[string]time.Time
	lastNode      string
	mutex         sync.RWMutex
	recoveryAfter time.Duration
}

func New(nodes []string) *Pool {
	return &Pool{
		nodes:         nodes,
		failedNodes:   make(map[string]time.Time),
		recoveryAfter: 10 * time.Second,
	}
}

// GetNext returns the next healthy node, falling back to any node if all are failed
func (p *Pool) GetNext() string {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if len(p.nodes) == 0 {
		return ""
	}

	// Try all nodes starting from current index
	startIdx := p.currentIdx
	for i := 0; i < len(p.nodes); i++ {
		idx := (startIdx + i) % len(p.nodes)
		node := p.nodes[idx]
		if p.isNodeHealthy(node) {
			p.currentIdx = idx
			return p.useNode(node)
		}
	}

	// All nodes are failed: reset and fallback
	slog.Warn("All RPC nodes failed, resetting failure states")
	p.failedNodes = make(map[string]time.Time)
	p.currentIdx = 0
	return p.useNode(p.nodes[0])
}

// MarkFailed marks a node as failed at current time
func (p *Pool) MarkFailed(node string) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.failedNodes[node] = time.Now()

	// If this is the current node, move to the next one
	if p.currentIdx < len(p.nodes) && p.nodes[p.currentIdx] == node {
		p.currentIdx = (p.currentIdx + 1) % len(p.nodes)
	}

	slog.Debug("Node marked as failed", "node", node)
}

// MarkHealthy removes node from failed list (if previously failed)
func (p *Pool) MarkHealthy(node string) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	if _, wasFailed := p.failedNodes[node]; wasFailed {
		delete(p.failedNodes, node)
		slog.Debug("Node marked as healthy", "node", node)
	}
}

// GetStats returns the total, healthy, and failed node counts
func (p *Pool) GetStats() (total, healthy, failed int) {
	p.mutex.RLock()
	defer p.mutex.RUnlock()
	total = len(p.nodes)
	failed = len(p.failedNodes)
	healthy = total - failed
	return
}

// isNodeHealthy returns true if node is not failed or has recovered
func (p *Pool) isNodeHealthy(node string) bool {
	if failTime, exists := p.failedNodes[node]; exists {
		return time.Since(failTime) > p.recoveryAfter
	}
	return true
}

// useNode sets node as current and logs switch if needed
func (p *Pool) useNode(node string) string {
	if p.lastNode != "" && p.lastNode != node {
		slog.Info("Switching RPC node", "from", p.lastNode, "to", node)
	}
	p.lastNode = node
	return node
}
