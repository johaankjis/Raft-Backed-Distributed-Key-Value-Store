.PHONY: build run test docker-build deploy clean

# Build the application
build:
	go build -o bin/raft-kv-store ./src/main.go

# Run a single node for testing
run:
	go run ./src/main.go --id=node-1 --port=8080 --http-port=9090 --data-dir=/tmp/raft-data --peers=node-2,node-3

# Run 3-node cluster locally
run-cluster:
	@echo "Starting 3-node Raft cluster..."
	@mkdir -p /tmp/raft-cluster
	go run ./src/main.go --id=node-1 --port=8081 --http-port=9091 --data-dir=/tmp/raft-cluster --peers=node-2,node-3 &
	go run ./src/main.go --id=node-2 --port=8082 --http-port=9092 --data-dir=/tmp/raft-cluster --peers=node-1,node-3 &
	go run ./src/main.go --id=node-3 --port=8083 --http-port=9093 --data-dir=/tmp/raft-cluster --peers=node-1,node-2 &
	@echo "Cluster started. Use 'make stop-cluster' to stop."

# Stop local cluster
stop-cluster:
	@pkill -f "raft-kv-store" || true
	@echo "Cluster stopped."

# Run tests
test:
	go test -v ./src/...

# Build Docker image
docker-build:
	docker build -t raft-kv-store:latest .

# Deploy to Kubernetes
deploy:
	kubectl apply -f deploy/statefulset.yaml
	kubectl apply -f deploy/service.yaml
	kubectl apply -f deploy/prometheus.yaml

# Check deployment status
status:
	kubectl get pods -l app=raft-kv-store
	kubectl get svc raft-kv-service

# View logs
logs:
	kubectl logs -l app=raft-kv-store --tail=100 -f

# Clean up
clean:
	rm -rf bin/
	rm -rf /tmp/raft-data
	rm -rf /tmp/raft-cluster

# Clean Kubernetes resources
clean-k8s:
	kubectl delete -f deploy/statefulset.yaml || true
	kubectl delete -f deploy/service.yaml || true
	kubectl delete -f deploy/prometheus.yaml || true
