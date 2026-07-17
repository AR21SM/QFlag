package raft

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

var ErrNotLeader = errors.New("not raft leader")

type persistedState struct {
	CurrentTerm int               `json:"currentTerm"`
	VotedFor    string            `json:"votedFor"`
	Log         []LogEntry        `json:"log"`
	CommitIndex int               `json:"commitIndex"`
	LastApplied int               `json:"lastApplied"`
	KV          map[string]string `json:"kv"`
}

type Node struct {
	mu              sync.Mutex
	id              string
	publicURL       string
	peers           map[string]string
	raftToken       string
	client          *http.Client
	dataFile        string
	currentTerm     int
	votedFor        string
	log             []LogEntry
	commitIndex     int
	lastApplied     int
	kv              map[string]string
	role            Role
	leaderID        string
	lastHeartbeat   time.Time
	electionReset   time.Time
	nextIndex       map[string]int
	matchIndex      map[string]int
	peerContact     map[string]time.Time
	applyCh         chan ApplyEvent
	stopCh          chan struct{}
	leaderChanges   atomic.Int64
	commitLatencyNs atomic.Int64
	commitCount     atomic.Int64
}

func NewNode(id, publicURL, dataDir string, peers map[string]string, raftToken string) (*Node, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}

	n := &Node{
		id:            id,
		publicURL:     publicURL,
		peers:         peers,
		raftToken:     raftToken,
		client:        &http.Client{Timeout: 900 * time.Millisecond},
		dataFile:      filepath.Join(dataDir, "raft-state.json"),
		kv:            map[string]string{},
		role:          RoleFollower,
		lastHeartbeat: time.Now(),
		electionReset: time.Now(),
		nextIndex:     map[string]int{},
		matchIndex:    map[string]int{},
		peerContact:   map[string]time.Time{},
		applyCh:       make(chan ApplyEvent, 1024),
		stopCh:        make(chan struct{}),
	}

	if err := n.load(); err != nil {
		return nil, err
	}

	return n, nil
}

func (n *Node) ID() string {
	return n.id
}

func (n *Node) ApplyEvents() <-chan ApplyEvent {
	return n.applyCh
}

func (n *Node) Start(ctx context.Context) {
	go n.electionLoop(ctx)
	go n.heartbeatLoop(ctx)
}

func (n *Node) Stop() {
	close(n.stopCh)
}

func (n *Node) LeaderID() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.leaderID
}

func (n *Node) Role() Role {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.role
}

func (n *Node) LeaderURL() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.leaderID == n.id {
		return n.publicURL
	}
	return n.peers[n.leaderID]
}

func (n *Node) Get(key string) (string, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	value, ok := n.kv[key]
	return value, ok
}

func (n *Node) List(prefix string) map[string]string {
	n.mu.Lock()
	defer n.mu.Unlock()

	out := map[string]string{}
	for key, value := range n.kv {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			out[key] = value
		}
	}
	return out
}

func (n *Node) Propose(ctx context.Context, command Command) (int, error) {
	start := time.Now()

	n.mu.Lock()
	if n.role != RoleLeader {
		n.mu.Unlock()
		return 0, ErrNotLeader
	}

	entry := LogEntry{Term: n.currentTerm, Command: command}
	n.log = append(n.log, entry)
	index := len(n.log) - 1
	n.matchIndex[n.id] = index
	n.nextIndex[n.id] = len(n.log)
	term := n.currentTerm
	if err := n.saveLocked(); err != nil {
		n.mu.Unlock()
		return 0, err
	}
	n.mu.Unlock()

	if !n.replicateUntilCommitted(ctx, index, term) {
		return 0, fmt.Errorf("raft commit failed: quorum unavailable")
	}

	n.commitLatencyNs.Add(time.Since(start).Nanoseconds())
	n.commitCount.Add(1)
	return index, nil
}

func (n *Node) HandleRequestVote(req RequestVoteRequest) RequestVoteResponse {
	n.mu.Lock()
	defer n.mu.Unlock()

	if req.Term < n.currentTerm {
		return RequestVoteResponse{Term: n.currentTerm, VoteGranted: false}
	}

	if req.Term > n.currentTerm {
		n.becomeFollowerLocked(req.Term, "")
	}

	lastIndex, lastTerm := n.lastLogInfoLocked()
	upToDate := req.LastLogTerm > lastTerm || (req.LastLogTerm == lastTerm && req.LastLogIndex >= lastIndex)
	canVote := n.votedFor == "" || n.votedFor == req.CandidateID
	if canVote && upToDate {
		n.votedFor = req.CandidateID
		n.electionReset = time.Now()
		_ = n.saveLocked()
		return RequestVoteResponse{Term: n.currentTerm, VoteGranted: true}
	}

	return RequestVoteResponse{Term: n.currentTerm, VoteGranted: false}
}

func (n *Node) HandleAppendEntries(req AppendEntriesRequest) AppendEntriesResponse {
	n.mu.Lock()
	defer n.mu.Unlock()

	if req.Term < n.currentTerm {
		return AppendEntriesResponse{Term: n.currentTerm, Success: false, MatchIndex: n.lastIndexLocked()}
	}

	if req.Term > n.currentTerm || n.role != RoleFollower {
		n.becomeFollowerLocked(req.Term, req.LeaderID)
	}

	n.leaderID = req.LeaderID
	n.lastHeartbeat = time.Now()
	n.electionReset = time.Now()

	if req.PrevLogIndex >= 0 {
		if req.PrevLogIndex >= len(n.log) || n.log[req.PrevLogIndex].Term != req.PrevLogTerm {
			return AppendEntriesResponse{Term: n.currentTerm, Success: false, MatchIndex: n.lastIndexLocked()}
		}
	}

	insertAt := req.PrevLogIndex + 1
	for i, entry := range req.Entries {
		target := insertAt + i
		if target < len(n.log) {
			if n.log[target].Term != entry.Term {
				n.log = n.log[:target]
				n.log = append(n.log, req.Entries[i:]...)
				break
			}
			continue
		}
		n.log = append(n.log, req.Entries[i:]...)
		break
	}

	if req.LeaderCommit > n.commitIndex {
		n.commitIndex = min(req.LeaderCommit, len(n.log)-1)
		n.applyCommittedLocked()
	}

	_ = n.saveLocked()
	return AppendEntriesResponse{Term: n.currentTerm, Success: true, MatchIndex: n.lastIndexLocked()}
}

func (n *Node) Status() ClusterStatus {
	n.mu.Lock()
	defer n.mu.Unlock()

	nodes := []NodeStatus{{
		ID:              n.id,
		Role:            n.role,
		Healthy:         true,
		Term:            n.currentTerm,
		CommitIndex:     n.commitIndex,
		LastApplied:     n.lastApplied,
		LastHeartbeatAt: n.lastHeartbeat,
		URL:             n.publicURL,
	}}

	healthyNodes := 1
	for id, url := range n.peers {
		lastContact := n.peerContact[id]
		healthy := !lastContact.IsZero() && time.Since(lastContact) < 2*time.Second
		if healthy {
			healthyNodes++
		}
		nodes = append(nodes, NodeStatus{
			ID:              id,
			Role:            peerRole(id, n.leaderID),
			Healthy:         healthy,
			Term:            n.currentTerm,
			CommitIndex:     n.matchIndex[id],
			LastApplied:     n.matchIndex[id],
			LastHeartbeatAt: lastContact,
			URL:             url,
		})
	}

	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })

	totalLatency := n.commitLatencyNs.Load()
	count := n.commitCount.Load()
	avg := "0ms"
	if count > 0 {
		avg = (time.Duration(totalLatency / count)).Round(time.Millisecond).String()
	}

	return ClusterStatus{
		Leader:           n.leaderID,
		Term:             n.currentTerm,
		CommitIndex:      n.commitIndex,
		HealthyNodes:     healthyNodes,
		TotalNodes:       len(nodes),
		LeaderChanges:    n.leaderChanges.Load(),
		AvgCommitLatency: avg,
		Nodes:            nodes,
	}
}

func (n *Node) Metrics() map[string]Metric {
	status := n.Status()
	return map[string]Metric{
		"raft_current_term":                 {Type: "gauge", Value: fmt.Sprintf("%d", status.Term)},
		"raft_commit_index":                 {Type: "gauge", Value: fmt.Sprintf("%d", status.CommitIndex)},
		"raft_leader_changes_total":         {Type: "counter", Value: fmt.Sprintf("%d", status.LeaderChanges)},
		"raft_healthy_nodes":                {Type: "gauge", Value: fmt.Sprintf("%d", status.HealthyNodes)},
		"raft_total_nodes":                  {Type: "gauge", Value: fmt.Sprintf("%d", status.TotalNodes)},
		"raft_commit_total":                 {Type: "counter", Value: fmt.Sprintf("%d", n.commitCount.Load())},
		"raft_commit_latency_seconds_total": {Type: "counter", Value: fmt.Sprintf("%.6f", float64(n.commitLatencyNs.Load())/float64(time.Second))},
	}
}

func (n *Node) electionLoop(ctx context.Context) {
	for {
		timeout := n.electionTimeout()
		select {
		case <-ctx.Done():
			return
		case <-n.stopCh:
			return
		case <-time.After(timeout):
			n.mu.Lock()
			elapsed := time.Since(n.electionReset)
			isLeader := n.role == RoleLeader
			n.mu.Unlock()
			if !isLeader && elapsed >= timeout {
				n.startElection(ctx)
			}
		}
	}
}

func (n *Node) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-n.stopCh:
			return
		case <-ticker.C:
			if n.Role() == RoleLeader {
				n.broadcastAppendEntries(ctx)
			}
		}
	}
}

func (n *Node) startElection(ctx context.Context) {
	n.mu.Lock()
	n.role = RoleCandidate
	n.currentTerm++
	term := n.currentTerm
	n.votedFor = n.id
	n.electionReset = time.Now()
	lastIndex, lastTerm := n.lastLogInfoLocked()
	_ = n.saveLocked()
	n.mu.Unlock()

	votes := int32(1)
	var wg sync.WaitGroup
	for peerID, peerURL := range n.peers {
		wg.Add(1)
		go func(id, url string) {
			defer wg.Done()
			resp, err := n.requestVote(ctx, url, RequestVoteRequest{
				Term:         term,
				CandidateID:  n.id,
				LastLogIndex: lastIndex,
				LastLogTerm:  lastTerm,
			})
			if err != nil {
				return
			}
			n.recordPeerContact(id)
			if resp.Term > term {
				n.mu.Lock()
				n.becomeFollowerLocked(resp.Term, "")
				n.mu.Unlock()
				return
			}
			if resp.VoteGranted {
				atomic.AddInt32(&votes, 1)
			}
			_ = id
		}(peerID, peerURL)
	}
	wg.Wait()

	if int(votes) >= n.quorum() {
		n.mu.Lock()
		if n.role == RoleCandidate && n.currentTerm == term {
			n.role = RoleLeader
			n.leaderID = n.id
			for id := range n.peers {
				n.nextIndex[id] = len(n.log)
				n.matchIndex[id] = -1
			}
			n.nextIndex[n.id] = len(n.log)
			n.matchIndex[n.id] = n.lastIndexLocked()
			n.leaderChanges.Add(1)
		}
		n.mu.Unlock()
		n.broadcastAppendEntries(ctx)
	}
}

func (n *Node) replicateUntilCommitted(ctx context.Context, index, term int) bool {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		n.broadcastAppendEntries(ctx)

		n.mu.Lock()
		if n.currentTerm != term || n.role != RoleLeader {
			n.mu.Unlock()
			return false
		}
		replicated := 1
		for id := range n.peers {
			if n.matchIndex[id] >= index {
				replicated++
			}
		}
		if replicated >= n.quorum() {
			if index > n.commitIndex && n.log[index].Term == n.currentTerm {
				n.commitIndex = index
				n.applyCommittedLocked()
				_ = n.saveLocked()
			}
			n.mu.Unlock()
			n.broadcastAppendEntries(ctx)
			return true
		}
		n.mu.Unlock()

		time.Sleep(35 * time.Millisecond)
	}
	return false
}

func (n *Node) broadcastAppendEntries(ctx context.Context) {
	n.mu.Lock()
	if n.role != RoleLeader {
		n.mu.Unlock()
		return
	}
	term := n.currentTerm
	commitIndex := n.commitIndex
	requests := make(map[string]AppendEntriesRequest, len(n.peers))
	for id := range n.peers {
		next := n.nextIndex[id]
		prev := next - 1
		prevTerm := 0
		if prev >= 0 && prev < len(n.log) {
			prevTerm = n.log[prev].Term
		}
		entries := append([]LogEntry(nil), n.log[next:]...)
		requests[id] = AppendEntriesRequest{
			Term:         term,
			LeaderID:     n.id,
			PrevLogIndex: prev,
			PrevLogTerm:  prevTerm,
			Entries:      entries,
			LeaderCommit: commitIndex,
		}
	}
	n.mu.Unlock()

	var wg sync.WaitGroup
	for id, req := range requests {
		peerURL := n.peers[id]
		wg.Add(1)
		go func(peerID, url string, payload AppendEntriesRequest) {
			defer wg.Done()
			resp, err := n.appendEntries(ctx, url, payload)
			if err != nil {
				return
			}
			n.recordPeerContact(peerID)

			n.mu.Lock()
			defer n.mu.Unlock()

			if resp.Term > n.currentTerm {
				n.becomeFollowerLocked(resp.Term, "")
				return
			}

			if n.role != RoleLeader || payload.Term != n.currentTerm {
				return
			}

			if resp.Success {
				n.matchIndex[peerID] = resp.MatchIndex
				n.nextIndex[peerID] = resp.MatchIndex + 1
			} else if n.nextIndex[peerID] > 0 {
				n.nextIndex[peerID]--
			}
		}(id, peerURL, req)
	}
	wg.Wait()
}

func (n *Node) requestVote(ctx context.Context, url string, req RequestVoteRequest) (RequestVoteResponse, error) {
	var resp RequestVoteResponse
	err := postJSON(ctx, n.client, url+"/raft/request-vote", n.raftToken, req, &resp)
	return resp, err
}

func (n *Node) appendEntries(ctx context.Context, url string, req AppendEntriesRequest) (AppendEntriesResponse, error) {
	var resp AppendEntriesResponse
	err := postJSON(ctx, n.client, url+"/raft/append-entries", n.raftToken, req, &resp)
	return resp, err
}

func postJSON(ctx context.Context, client *http.Client, url, raftToken string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if raftToken != "" {
		req.Header.Set("X-Raft-Token", raftToken)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected raft response: %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (n *Node) becomeFollowerLocked(term int, leaderID string) {
	n.role = RoleFollower
	n.currentTerm = term
	n.votedFor = ""
	n.leaderID = leaderID
	n.electionReset = time.Now()
	_ = n.saveLocked()
}

func (n *Node) applyCommittedLocked() {
	for n.lastApplied < n.commitIndex {
		n.lastApplied++
		entry := n.log[n.lastApplied]
		n.applyCommandLocked(n.lastApplied, entry.Command)
	}
}

func (n *Node) applyCommandLocked(index int, command Command) {
	if command.Op == "batch" {
		for _, operation := range command.Operations {
			n.applyCommandLocked(index, operation)
		}
		return
	}

	switch command.Op {
	case "put":
		n.kv[command.Key] = command.Value
	case "delete":
		delete(n.kv, command.Key)
	}
	select {
	case n.applyCh <- ApplyEvent{Index: index, Command: command}:
	default:
	}
}

func (n *Node) recordPeerContact(id string) {
	n.mu.Lock()
	n.peerContact[id] = time.Now()
	n.mu.Unlock()
}

func (n *Node) load() error {
	data, err := os.ReadFile(n.dataFile)
	if errors.Is(err, os.ErrNotExist) {
		n.log = []LogEntry{{Term: 0, Command: Command{Op: "noop"}}}
		n.commitIndex = 0
		n.lastApplied = 0
		return n.save()
	}
	if err != nil {
		return err
	}

	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	n.currentTerm = state.CurrentTerm
	n.votedFor = state.VotedFor
	n.log = state.Log
	n.commitIndex = state.CommitIndex
	n.lastApplied = state.LastApplied
	n.kv = state.KV
	if n.kv == nil {
		n.kv = map[string]string{}
	}
	if len(n.log) == 0 {
		n.log = []LogEntry{{Term: 0, Command: Command{Op: "noop"}}}
	}
	return nil
}

func (n *Node) save() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.saveLocked()
}

func (n *Node) saveLocked() error {
	state := persistedState{
		CurrentTerm: n.currentTerm,
		VotedFor:    n.votedFor,
		Log:         n.log,
		CommitIndex: n.commitIndex,
		LastApplied: n.lastApplied,
		KV:          n.kv,
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := n.dataFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, n.dataFile)
}

func (n *Node) lastLogInfoLocked() (int, int) {
	lastIndex := len(n.log) - 1
	lastTerm := 0
	if lastIndex >= 0 {
		lastTerm = n.log[lastIndex].Term
	}
	return lastIndex, lastTerm
}

func (n *Node) lastIndexLocked() int {
	return len(n.log) - 1
}

func (n *Node) quorum() int {
	return (len(n.peers)+1)/2 + 1
}

func (n *Node) electionTimeout() time.Duration {
	h := fnv.New32a()
	_, _ = h.Write([]byte(n.id))
	base := 220 + int(h.Sum32()%140)
	return time.Duration(base+rand.Intn(120)) * time.Millisecond
}

func peerRole(id, leaderID string) Role {
	if id == leaderID {
		return RoleLeader
	}
	return RoleFollower
}
