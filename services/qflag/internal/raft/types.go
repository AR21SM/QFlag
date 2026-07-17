package raft

import "time"

type Role string

const (
	RoleFollower  Role = "follower"
	RoleCandidate Role = "candidate"
	RoleLeader    Role = "leader"
)

type Command struct {
	Op         string    `json:"op"`
	Key        string    `json:"key,omitempty"`
	Value      string    `json:"value,omitempty"`
	Operations []Command `json:"operations,omitempty"`
}

type LogEntry struct {
	Term    int     `json:"term"`
	Command Command `json:"command"`
}

type RequestVoteRequest struct {
	Term         int    `json:"term"`
	CandidateID  string `json:"candidateId"`
	LastLogIndex int    `json:"lastLogIndex"`
	LastLogTerm  int    `json:"lastLogTerm"`
}

type RequestVoteResponse struct {
	Term        int  `json:"term"`
	VoteGranted bool `json:"voteGranted"`
}

type AppendEntriesRequest struct {
	Term         int        `json:"term"`
	LeaderID     string     `json:"leaderId"`
	PrevLogIndex int        `json:"prevLogIndex"`
	PrevLogTerm  int        `json:"prevLogTerm"`
	Entries      []LogEntry `json:"entries"`
	LeaderCommit int        `json:"leaderCommit"`
}

type AppendEntriesResponse struct {
	Term       int  `json:"term"`
	Success    bool `json:"success"`
	MatchIndex int  `json:"matchIndex"`
}

type NodeStatus struct {
	ID              string    `json:"id"`
	Role            Role      `json:"role"`
	Healthy         bool      `json:"healthy"`
	Term            int       `json:"term"`
	CommitIndex     int       `json:"commitIndex"`
	LastApplied     int       `json:"lastApplied"`
	LastHeartbeatAt time.Time `json:"lastHeartbeatAt"`
	URL             string    `json:"url"`
}

type ClusterStatus struct {
	Leader           string       `json:"leader"`
	Term             int          `json:"term"`
	CommitIndex      int          `json:"commitIndex"`
	HealthyNodes     int          `json:"healthyNodes"`
	TotalNodes       int          `json:"totalNodes"`
	LeaderChanges    int64        `json:"leaderChanges"`
	AvgCommitLatency string       `json:"avgCommitLatency"`
	Nodes            []NodeStatus `json:"nodes"`
}

type ApplyEvent struct {
	Index   int     `json:"index"`
	Command Command `json:"command"`
}

type Metric struct {
	Type  string
	Value string
}
