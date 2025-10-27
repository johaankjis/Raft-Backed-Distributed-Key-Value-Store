package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"raft-kv-store/src/raft"
	"raft-kv-store/src/storage"
)

// KVServer implements the gRPC KVStore service
type KVServer struct {
	node    *raft.Node
	storage *storage.Storage
	port    int
}

// NewKVServer creates a new KV server
func NewKVServer(node *raft.Node, storage *storage.Storage, port int) *KVServer {
	return &KVServer{
		node:    node,
		storage: storage,
		port:    port,
	}
}

// Start starts the gRPC server
func (s *KVServer) Start() error {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", s.port))
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	grpcServer := grpc.NewServer()
	// Register services here (proto generated code would be used)
	
	log.Printf("KV Server listening on port %d", s.port)
	return grpcServer.Serve(lis)
}

// Put handles PUT requests
func (s *KVServer) Put(ctx context.Context, key string, value []byte) (uint64, error) {
	cmd := raft.Command{
		Type:  "PUT",
		Key:   key,
		Value: value,
	}

	index, err := s.node.Propose(cmd)
	if err != nil {
		return 0, status.Errorf(codes.FailedPrecondition, "not leader: %v", err)
	}

	// Wait for commit (with timeout)
	timeout := time.After(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			return 0, status.Errorf(codes.DeadlineExceeded, "operation timeout")
		case <-ticker.C:
			state, _, _ := s.node.GetState()
			if state != raft.Leader {
				return 0, status.Errorf(codes.FailedPrecondition, "no longer leader")
			}
			// Check if committed (simplified check)
			if s.isCommitted(index) {
				return index, nil
			}
		}
	}
}

// Get handles GET requests
func (s *KVServer) Get(ctx context.Context, key string) ([]byte, bool, error) {
	// Read from local storage (linearizable reads would check leader status)
	state, _, _ := s.node.GetState()
	if state != raft.Leader {
		return nil, false, status.Errorf(codes.FailedPrecondition, "not leader")
	}

	value, found, err := s.storage.Get(key)
	if err != nil {
		return nil, false, status.Errorf(codes.Internal, "storage error: %v", err)
	}

	return value, found, nil
}

// Delete handles DELETE requests
func (s *KVServer) Delete(ctx context.Context, key string) (uint64, error) {
	cmd := raft.Command{
		Type: "DELETE",
		Key:  key,
	}

	index, err := s.node.Propose(cmd)
	if err != nil {
		return 0, status.Errorf(codes.FailedPrecondition, "not leader: %v", err)
	}

	// Wait for commit
	timeout := time.After(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			return 0, status.Errorf(codes.DeadlineExceeded, "operation timeout")
		case <-ticker.C:
			if s.isCommitted(index) {
				return index, nil
			}
		}
	}
}

// GetClusterStatus returns cluster status information
func (s *KVServer) GetClusterStatus() map[string]interface{} {
	state, term, leaderID := s.node.GetState()
	metrics := s.node.GetMetrics()

	metrics.mu.RLock()
	defer metrics.mu.RUnlock()

	return map[string]interface{}{
		"state":             string(state),
		"term":              term,
		"leader_id":         leaderID,
		"leader_elections":  metrics.LeaderElections,
		"append_entries":    metrics.AppendEntriesRPC,
		"committed_entries": metrics.CommittedEntries,
		"last_heartbeat":    metrics.LastHeartbeat.Format(time.RFC3339),
	}
}

// isCommitted checks if a log entry is committed (simplified)
func (s *KVServer) isCommitted(index uint64) bool {
	// In a real implementation, this would check the commit index
	// For demo purposes, we simulate with a delay
	time.Sleep(50 * time.Millisecond)
	return true
}

// ServeHTTP provides a simple HTTP interface for testing
func (s *KVServer) ServeHTTP(port int) error {
	// Simple HTTP server for demo/testing
	log.Printf("HTTP API server would listen on port %d", port)
	return nil
}

// PrometheusMetrics returns metrics in Prometheus format
func (s *KVServer) PrometheusMetrics() string {
	metrics := s.node.GetMetrics()
	state, term, _ := s.node.GetState()

	metrics.mu.RLock()
	defer metrics.mu.RUnlock()

	return fmt.Sprintf(`# HELP raft_leader_elections_total Total number of leader elections
# TYPE raft_leader_elections_total counter
raft_leader_elections_total %d

# HELP raft_append_entries_total Total number of AppendEntries RPCs
# TYPE raft_append_entries_total counter
raft_append_entries_total %d

# HELP raft_committed_entries_total Total number of committed log entries
# TYPE raft_committed_entries_total counter
raft_committed_entries_total %d

# HELP raft_current_term Current Raft term
# TYPE raft_current_term gauge
raft_current_term %d

# HELP raft_is_leader Whether this node is the leader (1) or not (0)
# TYPE raft_is_leader gauge
raft_is_leader %d
`,
		metrics.LeaderElections,
		metrics.AppendEntriesRPC,
		metrics.CommittedEntries,
		term,
		boolToInt(state == raft.Leader),
	)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// BatchRequest represents a batch of operations
type BatchRequest struct {
	Operations []Operation `json:"operations"`
}

type Operation struct {
	Type  string `json:"type"`  // "PUT" or "DELETE"
	Key   string `json:"key"`
	Value []byte `json:"value,omitempty"`
}

// BatchPut handles batched PUT operations for higher throughput
func (s *KVServer) BatchPut(ctx context.Context, ops []Operation) ([]uint64, error) {
	indices := make([]uint64, 0, len(ops))

	for _, op := range ops {
		cmd := raft.Command{
			Type:  op.Type,
			Key:   op.Key,
			Value: op.Value,
		}

		index, err := s.node.Propose(cmd)
		if err != nil {
			return nil, err
		}
		indices = append(indices, index)
	}

	return indices, nil
}

// HealthCheck returns the health status of the node
func (s *KVServer) HealthCheck() map[string]interface{} {
	state, _, leaderID := s.node.GetState()
	
	return map[string]interface{}{
		"status":    "healthy",
		"state":     string(state),
		"leader_id": leaderID,
		"timestamp": time.Now().Format(time.RFC3339),
	}
}

// MarshalJSON helper for JSON responses
func MarshalJSON(v interface{}) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}
