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
	lastNode    string // Track the last used node
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

	// Check if current node is healthy
	currentNode := p.nodes[p.currentIdx]
	if failTime, exists := p.failedNodes[currentNode]; !exists || time.Since(failTime) > 30*time.Second {
		// Current node is healthy, use it
		if p.lastNode != "" && p.lastNode != currentNode {
			slog.Info("Switching RPC node", "from", p.lastNode, "to", currentNode)
		}
		p.lastNode = currentNode
		return currentNode
	}

	// Current node is failed, find next healthy node
	for i := 0; i < len(p.nodes); i++ {
		p.currentIdx = (p.currentIdx + 1) % len(p.nodes)
		node := p.nodes[p.currentIdx]

		// Check if node is healthy (not failed or recovery time passed)
		if failTime, exists := p.failedNodes[node]; !exists || time.Since(failTime) > 30*time.Second {
			// Log when switching to a different node
			if p.lastNode != "" && p.lastNode != node {
				slog.Info("Switching RPC node", "from", p.lastNode, "to", node)
			}
			p.lastNode = node
			return node
		}
	}

	// If all nodes are failed, return the first one and reset failures
	p.failedNodes = make(map[string]time.Time)
	p.currentIdx = 0
	node := p.nodes[0]
	if p.lastNode != "" && p.lastNode != node {
		slog.Info("Switching RPC node (all nodes were failed)", "from", p.lastNode, "to", node)
	}
	p.lastNode = node
	return node
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

	// Only log if node was previously marked failed
	if _, exists := p.failedNodes[node]; exists {
		slog.Debug("Node marked as healthy", "node", node)
	}
	delete(p.failedNodes, node)
}

func (p *Pool) GetStats() (total, healthy, failed int) {
	p.mutex.RLock()
	defer p.mutex.RUnlock()
	total = len(p.nodes)
	failed = len(p.failedNodes)
	healthy = total - failed
	return
}
