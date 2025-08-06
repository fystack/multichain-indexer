package pool

import (
	"testing"
)

func TestPoolNodeSwitching(t *testing.T) {
	nodes := []string{
		"https://node1.example.com",
		"https://node2.example.com",
		"https://node3.example.com",
	}

	p := New(nodes)

	// Test initial node selection
	node1 := p.GetNext()
	if node1 != nodes[0] {
		t.Errorf("Expected first node %s, got %s", nodes[0], node1)
	}

	// Mark first node as failed
	p.MarkFailed(nodes[0])

	// Get next node - should be node2
	node2 := p.GetNext()
	if node2 != nodes[1] {
		t.Errorf("Expected second node %s after failure, got %s", nodes[1], node2)
	}

	// Mark second node as failed
	p.MarkFailed(nodes[1])

	// Get next node - should be node3
	node3 := p.GetNext()
	if node3 != nodes[2] {
		t.Errorf("Expected third node %s after second failure, got %s", nodes[2], node3)
	}

	// Mark third node as failed
	p.MarkFailed(nodes[2])

	// All nodes failed, should reset and return first node
	node4 := p.GetNext()
	if node4 != nodes[0] {
		t.Errorf("Expected first node after all failures, got %s", node4)
	}
}

func TestPoolRecovery(t *testing.T) {
	nodes := []string{
		"https://node1.example.com",
		"https://node2.example.com",
	}

	p := New(nodes)

	// Mark first node as failed
	p.MarkFailed(nodes[0])

	// Should get second node
	node1 := p.GetNext()
	if node1 != nodes[1] {
		t.Errorf("Expected second node after failure, got %s", node1)
	}

	// Mark second node as failed
	p.MarkFailed(nodes[1])

	// Should reset and return first node
	node2 := p.GetNext()
	if node2 != nodes[0] {
		t.Errorf("Expected first node after all failures, got %s", node2)
	}

	// Mark first node as healthy again
	p.MarkHealthy(nodes[0])

	// Should return first node since it's healthy
	node3 := p.GetNext()
	if node3 != nodes[0] {
		t.Errorf("Expected first node after recovery, got %s", node3)
	}
}
