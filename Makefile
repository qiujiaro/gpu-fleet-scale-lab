.PHONY: build test vet fmt preflight-day2 exp2-preview exp2p-smoke exp2p-load-test smoke figures clean

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

# Destructive to the disposable test cluster: creates a dedicated namespace and one
# 3-GPU KWOK node, compares default partial placement with TopoGang all-or-nothing.
exp2-preview:
	./scripts/exp2-gang-preview.sh

# End-to-end TopoGang profiler smoke against the current disposable KWOK cluster.
# Requires the second scheduler on https://127.0.0.1:10260.
exp2p-smoke:
	./scripts/exp2p-smoke.sh

# Stepped TopoGang load matrix: QPS 4/8/16/32/64, three runs per level.
exp2p-load-test:
	./scripts/exp2p-load-test.sh

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
