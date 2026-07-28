package topogang

import (
	"fmt"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	defaultPermitTimeout = 30 * time.Second
	defaultTopologyKey   = "topology.nvidia.com/nvlink-domain"
)

const defaultGPUResourceName v1.ResourceName = "nvidia.com/gpu"

// TopoGangArgs is the user-configurable part of the TopoGang plugin.
//
// Duration uses metav1.Duration so scheduler YAML can express it as a readable value
// such as "30s". ResourceName is kept as the Kubernetes type so downstream resource
// lookups cannot accidentally use an unrelated string.
type TopoGangArgs struct {
	PermitTimeout   metav1.Duration `json:"permitTimeout,omitempty"`
	TopologyKey     string          `json:"topologyKey,omitempty"`
	GPUResourceName v1.ResourceName `json:"gpuResourceName,omitempty"`
}

func defaultTopoGangArgs() TopoGangArgs {
	return TopoGangArgs{
		PermitTimeout:   metav1.Duration{Duration: defaultPermitTimeout},
		TopologyKey:     defaultTopologyKey,
		GPUResourceName: defaultGPUResourceName,
	}
}

func (a TopoGangArgs) validate() error {
	if a.PermitTimeout.Duration <= 0 {
		return fmt.Errorf("permitTimeout must be greater than zero")
	}
	if errs := validation.IsQualifiedName(a.TopologyKey); len(errs) != 0 {
		return fmt.Errorf("topologyKey %q is invalid: %s", a.TopologyKey, errs[0])
	}
	if errs := validation.IsQualifiedName(string(a.GPUResourceName)); len(errs) != 0 {
		return fmt.Errorf("gpuResourceName %q is invalid: %s", a.GPUResourceName, errs[0])
	}
	return nil
}
