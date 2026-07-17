package raft

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestClusterCommitsAfterLeaderFailover(t *testing.T) {
	t.Parallel()

	cluster := newTestCluster(t, []string{"node-1", "node-2", "node-3"})
	defer cluster.close()

	leader := cluster.waitForLeader(t, "")
	if _, err := leader.Propose(context.Background(), Command{Op: "put", Key: "/flags/checkout/rollout", Value: "5"}); err != nil {
		t.Fatalf("initial commit failed: %v", err)
	}
	cluster.waitForValue(t, "/flags/checkout/rollout", "5")

	stoppedLeaderID := leader.ID()
	cluster.stop(stoppedLeaderID)

	newLeader := cluster.waitForLeader(t, stoppedLeaderID)
	if _, err := newLeader.Propose(context.Background(), Command{Op: "put", Key: "/flags/checkout/rollout", Value: "25"}); err != nil {
		t.Fatalf("post-failover commit failed: %v", err)
	}
	cluster.waitForValue(t, "/flags/checkout/rollout", "25")
}

type testCluster struct {
	nodes   map[string]*Node
	servers map[string]*http.Server
	cancels map[string]context.CancelFunc
}

func newTestCluster(t *testing.T, ids []string) *testCluster {
	t.Helper()

	listeners := map[string]net.Listener{}
	urls := map[string]string{}
	for _, id := range ids {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners[id] = listener
		urls[id] = "http://" + listener.Addr().String()
	}

	cluster := &testCluster{
		nodes:   map[string]*Node{},
		servers: map[string]*http.Server{},
		cancels: map[string]context.CancelFunc{},
	}

	for _, id := range ids {
		peers := map[string]string{}
		for peerID, url := range urls {
			if peerID != id {
				peers[peerID] = url
			}
		}

		node, err := NewNode(id, urls[id], t.TempDir(), peers, "")
		if err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		node.Start(ctx)
		cluster.nodes[id] = node
		cluster.cancels[id] = cancel

		mux := http.NewServeMux()
		mux.HandleFunc("/raft/request-vote", func(w http.ResponseWriter, r *http.Request) {
			var req RequestVoteRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			_ = json.NewEncoder(w).Encode(node.HandleRequestVote(req))
		})
		mux.HandleFunc("/raft/append-entries", func(w http.ResponseWriter, r *http.Request) {
			var req AppendEntriesRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			_ = json.NewEncoder(w).Encode(node.HandleAppendEntries(req))
		})

		server := &http.Server{Handler: mux}
		cluster.servers[id] = server
		go func(listener net.Listener) {
			_ = server.Serve(listener)
		}(listeners[id])
	}

	return cluster
}

func (c *testCluster) close() {
	for id := range c.nodes {
		c.stop(id)
	}
}

func (c *testCluster) stop(id string) {
	if cancel, ok := c.cancels[id]; ok {
		cancel()
		delete(c.cancels, id)
	}
	if server, ok := c.servers[id]; ok {
		_ = server.Close()
		delete(c.servers, id)
	}
	delete(c.nodes, id)
}

func (c *testCluster) waitForLeader(t *testing.T, excluded string) *Node {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for id, node := range c.nodes {
			if id == excluded {
				continue
			}
			if node.Role() == RoleLeader {
				return node
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("leader not elected")
	return nil
}

func (c *testCluster) waitForValue(t *testing.T, key, expected string) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		matches := 0
		for _, node := range c.nodes {
			value, ok := node.Get(key)
			if ok && value == expected {
				matches++
			}
		}
		if matches >= 2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("value %s did not replicate to quorum", expected)
}
