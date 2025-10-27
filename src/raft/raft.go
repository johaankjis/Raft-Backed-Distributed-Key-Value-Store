package raft

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"time"

	"raft-kv-store/src/storage"
)

// NodeState represents the state of a Raft node
type NodeState string

const (
	Follower  NodeState = "follower"
	Candidate NodeState = "candidate"
	Leader    NodeState = "leader"
)

// Command represents a state machine command
type Command struct {
	Type  string `json:"type"`  // "PUT" or "DELETE"
	Key   string `json:"key"`
	Value []byte `json:"value"`
}

// Node represents a Raft consensus node
type Node struct {
	mu sync.RWMutex

	// Persistent state
	id          string
	currentTerm uint64
	votedFor    string
	log         *storage.RaftLog

	// Volatile state
	state       NodeState
	commitIndex uint64
	lastApplied uint64

	// Leader state
	nextIndex  map[string]uint64
	matchIndex map[string]uint64

	// Cluster configuration
	peers     []string
	leaderID  string

	// Channels
	applyCh       chan ApplyMsg
	heartbeatCh   chan bool
	electionTimer *time.Timer

	// Storage
	storage *storage.Storage

	// Metrics
	metrics *Metrics
}

// ApplyMsg represents a committed log entry to apply
type ApplyMsg struct {
	Index   uint64
	Command Command
}

// Metrics tracks Raft performance metrics
type Metrics struct {
	mu                sync.RWMutex
	LeaderElections   uint64
	AppendEntriesRPC  uint64
	RequestVoteRPC    uint64
	CommittedEntries  uint64
	LastHeartbeat     time.Time
}

// NewNode creates a new Raft node
func NewNode(id string, peers []string, dataDir string) (*Node, error) {
	raftLog, err := storage.NewRaftLog(dataDir)
	if err != nil {
		return nil, err
	}

	store, err := storage.NewStorage(dataDir + "/data")
	if err != nil {
		return nil, err
	}

	// Load persistent state
	currentTerm, _ := raftLog.GetMetadata("currentTerm")
	
	node := &Node{
		id:          id,
		currentTerm: currentTerm,
		log:         raftLog,
		state:       Follower,
		peers:       peers,
		applyCh:     make(chan ApplyMsg, 100),
		heartbeatCh: make(chan bool, 10),
		storage:     store,
		nextIndex:   make(map[string]uint64),
		matchIndex:  make(map[string]uint64),
		metrics:     &Metrics{},
	}

	return node, nil
}

// Start begins the Raft consensus algorithm
func (n *Node) Start(ctx context.Context) {
	go n.electionLoop(ctx)
	go n.applyLoop(ctx)
	log.Printf("[%s] Raft node started as %s", n.id, n.state)
}

// electionLoop manages leader election and heartbeats
func (n *Node) electionLoop(ctx context.Context) {
	n.resetElectionTimer()

	for {
		select {
		case <-ctx.Done():
			return
		case <-n.electionTimer.C:
			n.startElection()
		case <-n.heartbeatCh:
			n.resetElectionTimer()
		}
	}
}

// resetElectionTimer resets the election timeout
func (n *Node) resetElectionTimer() {
	timeout := time.Duration(150+rand.Intn(150)) * time.Millisecond
	if n.electionTimer == nil {
		n.electionTimer = time.NewTimer(timeout)
	} else {
		n.electionTimer.Reset(timeout)
	}
}

// startElection initiates a leader election
func (n *Node) startElection() {
	n.mu.Lock()
	n.state = Candidate
	n.currentTerm++
	n.votedFor = n.id
	currentTerm := n.currentTerm
	n.log.SetMetadata("currentTerm", n.currentTerm)
	n.metrics.mu.Lock()
	n.metrics.LeaderElections++
	n.metrics.mu.Unlock()
	n.mu.Unlock()

	log.Printf("[%s] Starting election for term %d", n.id, currentTerm)

	votes := 1 // Vote for self
	var voteMu sync.Mutex

	for _, peer := range n.peers {
		go func(peerID string) {
			// Simulate RequestVote RPC
			granted := n.simulateRequestVote(peerID, currentTerm)
			
			voteMu.Lock()
			if granted {
				votes++
			}
			voteMu.Unlock()

			// Check if we won the election
			voteMu.Lock()
			if votes > len(n.peers)/2 && n.state == Candidate {
				n.becomeLeader()
			}
			voteMu.Unlock()
		}(peer)
	}

	n.resetElectionTimer()
}

// becomeLeader transitions the node to leader state
func (n *Node) becomeLeader() {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.state = Leader
	n.leaderID = n.id
	
	// Initialize leader state
	lastIndex, _ := n.log.GetLastIndex()
	for _, peer := range n.peers {
		n.nextIndex[peer] = lastIndex + 1
		n.matchIndex[peer] = 0
	}

	log.Printf("[%s] Became leader for term %d", n.id, n.currentTerm)

	// Start sending heartbeats
	go n.sendHeartbeats()
}

// sendHeartbeats sends periodic heartbeats to followers
func (n *Node) sendHeartbeats() {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		n.mu.RLock()
		if n.state != Leader {
			n.mu.RUnlock()
			return
		}
		n.mu.RUnlock()

		for _, peer := range n.peers {
			go n.sendAppendEntries(peer)
		}

		<-ticker.C
	}
}

// sendAppendEntries sends AppendEntries RPC to a peer
func (n *Node) sendAppendEntries(peer string) {
	n.mu.RLock()
	if n.state != Leader {
		n.mu.RUnlock()
		return
	}
	
	nextIdx := n.nextIndex[peer]
	prevLogIndex := nextIdx - 1
	n.mu.RUnlock()

	// Get entries to send
	entries := []storage.LogEntry{}
	lastIndex, _ := n.log.GetLastIndex()
	
	for i := nextIdx; i <= lastIndex; i++ {
		entry, err := n.log.GetEntry(i)
		if err == nil && entry != nil {
			entries = append(entries, *entry)
		}
	}

	// Simulate AppendEntries RPC
	success := n.simulateAppendEntries(peer, prevLogIndex, entries)

	n.mu.Lock()
	if success {
		if len(entries) > 0 {
			n.nextIndex[peer] = entries[len(entries)-1].Index + 1
			n.matchIndex[peer] = entries[len(entries)-1].Index
		}
		n.updateCommitIndex()
	} else {
		// Decrement nextIndex and retry
		if n.nextIndex[peer] > 1 {
			n.nextIndex[peer]--
		}
	}
	n.mu.Unlock()

	n.metrics.mu.Lock()
	n.metrics.AppendEntriesRPC++
	n.metrics.LastHeartbeat = time.Now()
	n.metrics.mu.Unlock()
}

// updateCommitIndex updates the commit index based on majority replication
func (n *Node) updateCommitIndex() {
	if n.state != Leader {
		return
	}

	lastIndex, _ := n.log.GetLastIndex()
	for i := n.commitIndex + 1; i <= lastIndex; i++ {
		replicated := 1 // Count self
		for _, peer := range n.peers {
			if n.matchIndex[peer] >= i {
				replicated++
			}
		}

		if replicated > len(n.peers)/2 {
			n.commitIndex = i
			n.metrics.mu.Lock()
			n.metrics.CommittedEntries++
			n.metrics.mu.Unlock()
		}
	}
}

// applyLoop applies committed entries to the state machine
func (n *Node) applyLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Millisecond):
			n.mu.Lock()
			for n.lastApplied < n.commitIndex {
				n.lastApplied++
				entry, err := n.log.GetEntry(n.lastApplied)
				if err != nil || entry == nil {
					continue
				}

				var cmd Command
				if err := json.Unmarshal(entry.Command, &cmd); err != nil {
					log.Printf("[%s] Failed to unmarshal command: %v", n.id, err)
					continue
				}

				// Apply to state machine
				switch cmd.Type {
				case "PUT":
					n.storage.Put(cmd.Key, cmd.Value)
				case "DELETE":
					n.storage.Delete(cmd.Key)
				}

				n.applyCh <- ApplyMsg{
					Index:   entry.Index,
					Command: cmd,
				}
			}
			n.mu.Unlock()
		}
	}
}

// Propose proposes a new command to the Raft cluster
func (n *Node) Propose(cmd Command) (uint64, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.state != Leader {
		return 0, fmt.Errorf("not leader, current leader: %s", n.leaderID)
	}

	lastIndex, _ := n.log.GetLastIndex()
	index := lastIndex + 1

	cmdData, err := json.Marshal(cmd)
	if err != nil {
		return 0, err
	}

	entry := storage.LogEntry{
		Index:       index,
		Term:        n.currentTerm,
		Command:     cmdData,
		CommandType: cmd.Type,
	}

	if err := n.log.AppendEntry(entry); err != nil {
		return 0, err
	}

	log.Printf("[%s] Proposed command %s at index %d", n.id, cmd.Type, index)
	return index, nil
}

// GetState returns the current state of the node
func (n *Node) GetState() (NodeState, uint64, string) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.state, n.currentTerm, n.leaderID
}

// simulateRequestVote simulates a RequestVote RPC (for demo purposes)
func (n *Node) simulateRequestVote(peer string, term uint64) bool {
	// In a real implementation, this would make a gRPC call
	// For demo, we simulate a successful vote with 80% probability
	return rand.Float32() < 0.8
}

// simulateAppendEntries simulates an AppendEntries RPC (for demo purposes)
func (n *Node) simulateAppendEntries(peer string, prevLogIndex uint64, entries []storage.LogEntry) bool {
	// In a real implementation, this would make a gRPC call
	// For demo, we simulate success with 90% probability
	return rand.Float32() < 0.9
}

// GetMetrics returns current metrics
func (n *Node) GetMetrics() *Metrics {
	return n.metrics
}

// Close shuts down the node
func (n *Node) Close() error {
	n.log.Close()
	return n.storage.Close()
}
