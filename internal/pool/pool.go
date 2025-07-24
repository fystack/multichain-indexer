package pool

import (
	"log/slog"
	"sync"
	"time"
)

type Pool struct {
	nodes       []string
	currentIdx  int
	failedNodes map[string]time.Time
	mutex       sync.RWMutex
}

func New(nodes []string) *Pool {
	return &Pool{
		nodes:       nodes,
		failedNodes: make(map[string]time.Time),
	}
}

func (p *Pool) GetNext() string {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if len(p.nodes) == 0 {
		return ""
	}

	// Find next healthy node
	for i := 0; i < len(p.nodes); i++ {
		node := p.nodes[p.currentIdx]
		p.currentIdx = (p.currentIdx + 1) % len(p.nodes)

		// Check if node is healthy (not failed or recovery time passed)
		if failTime, exists := p.failedNodes[node]; !exists || time.Since(failTime) > 30*time.Second {
			return node
		}
	}

	// If all nodes are failed, return the first one and reset failures
	p.failedNodes = make(map[string]time.Time)
	return p.nodes[0]
}

func (p *Pool) MarkFailed(node string) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	p.failedNodes[node] = time.Now()
	slog.Debug("Node marked as failed", "node", node)
}

func (p *Pool) MarkHealthy(node string) {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	delete(p.failedNodes, node)
	slog.Debug("Node marked as healthy", "node", node)
}

func (p *Pool) GetStats() (total, healthy, failed int) {
	p.mutex.RLock()
	defer p.mutex.RUnlock()
	total = len(p.nodes)
	failed = len(p.failedNodes)
	healthy = total - failed
	return
}
