.PHONY: build test vet fmt smoke clean

build:
	go build ./...

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

# 60s micro scale smoke (Day 9): 50 nodes / 20 pods, structural assertions only,
# no SLO numeric assertions.
smoke:
	@echo "TODO(Day9): kwokctl create + spawn 50 + loadgen 20 + assert all scheduled"

clean:
	kwokctl delete cluster --name gpu-scale || true
	rm -rf analysis/figures/*.png experiments/_raw/*.jsonl
