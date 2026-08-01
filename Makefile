.PHONY: build test vet fmt exp0-loadgen-calibration exp1-fleet-readiness exp2-two-schedulers exp2-topogang-preview exp2-topogang-smoke exp2-topogang-load-test smoke figures clean

build:
	go build ./...

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

# Exp0: validate load-generator throughput against a running 1000-node cluster.
exp0-loadgen-calibration:
	./scripts/exp0-loadgen-calibration.sh

# Exp2 phase A: verify default and TopoGang schedulers handle only their own Pods.
exp2-two-schedulers:
	./scripts/exp2-two-schedulers.sh

# Exp1: scale an existing KWOK fleet and record time to Ready.
exp1-fleet-readiness:
	./scripts/exp1-fleet-readiness.sh

# Destructive to the disposable test cluster: creates a dedicated namespace and one
# 3-GPU KWOK node, compares default partial placement with TopoGang all-or-nothing.
exp2-topogang-preview:
	./scripts/exp2-topogang-preview.sh

# End-to-end TopoGang profiler smoke against the current disposable KWOK cluster.
# Requires the second scheduler on https://127.0.0.1:10260.
exp2-topogang-smoke:
	./scripts/exp2-topogang-smoke.sh

# Stepped TopoGang load matrix: QPS 4/8/16/32/64, three runs per level.
exp2-topogang-load-test:
	./scripts/exp2-topogang-load-test.sh

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
	rm -rf analysis/figures/*.png experiments/diagnostics/local/*.jsonl
