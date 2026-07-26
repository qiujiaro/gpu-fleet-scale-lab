.PHONY: build test vet fmt preflight-day2 smoke figures clean

build:
	go build ./...

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

# Exp3 client calibration: requires an already-running 1000-node cluster.
preflight-day2:
	./scripts/day2-client-preflight.sh

# Regenerate every figure from the CSVs in experiments/. Figures whose input data does
# not exist yet are skipped with a reason; nothing is drawn from placeholder numbers.
figures:
	python3 analysis/plot.py --experiments experiments --out analysis/figures

# 60s micro scale smoke (Day 9): 50 nodes / 20 pods, structural assertions only,
# no SLO numeric assertions.
smoke:
	@echo "TODO(Day9): kwokctl create + spawn 50 + loadgen 20 + assert all scheduled"

clean:
	kwokctl delete cluster --name gpu-scale || true
	rm -rf analysis/figures/*.png experiments/_raw/*.jsonl
