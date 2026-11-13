package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	DefaultClusterName = "microkind"
	DefaultK0sVersion  = "v1.37.2+k0s.0"
	DefaultImage       = "localhost:5000/fckind/node:v1.37.2-k0s.0"
	DefaultPodSubnet   = "10.244.0.0/16"
	DefaultSvcSubnet   = "10.96.0.0/12"
	DefaultCNI         = "kube-router"
)

type NodeRole string

const (
	RoleControlPlane NodeRole = "control-plane"
	RoleWorker       NodeRole = "worker"
)

// ClusterConfig represents the declarative cluster specification
type ClusterConfig struct {
	Kind       string           `yaml:"kind"`
	APIVersion string           `yaml:"apiVersion"`
	Name       string           `yaml:"name"`
	Spec       *SpecConfig      `yaml:"spec,omitempty"`
	Networking NetworkingConfig `yaml:"networking"`
	K0s        K0sConfig        `yaml:"k0s"`
	Nodes      []NodeConfig     `yaml:"nodes"`
}

// SpecConfig defines optional Kubernetes-style spec settings
type SpecConfig struct {
	Network *SpecNetworkConfig `yaml:"network,omitempty"`
}

// SpecNetworkConfig defines network settings under spec
type SpecNetworkConfig struct {
	KubeProxy *KubeProxyConfig `yaml:"kubeProxy,omitempty"`
}

// NetworkingConfig defines cluster networking options
type NetworkingConfig struct {
	PodSubnet     string          `yaml:"podSubnet"`
	ServiceSubnet string          `yaml:"serviceSubnet"`
	CNI           CNIConfig       `yaml:"cni"`
	KubeProxy     KubeProxyConfig `yaml:"kubeProxy,omitempty"`
	APIServerPort int             `yaml:"apiServerPort,omitempty"`
}

// KubeProxyConfig defines cluster kube-proxy options
type KubeProxyConfig struct {
	Disabled bool   `yaml:"disabled"`
	Mode     string `yaml:"mode,omitempty"`
}

// UnmarshalYAML supports both mapping format (disabled: true) and scalar string (disabled / none / off)
func (k *KubeProxyConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		val := strings.ToLower(strings.TrimSpace(value.Value))
		if val == "disabled" || val == "none" || val == "false" || val == "off" {
			k.Disabled = true
			return nil
		}
		if val == "enabled" || val == "true" || val == "on" {
			k.Disabled = false
			return nil
		}
		k.Mode = val
		return nil
	}
	type rawKubeProxy KubeProxyConfig
	var raw rawKubeProxy
	if err := value.Decode(&raw); err != nil {
		return err
	}
	*k = KubeProxyConfig(raw)
	return nil
}

// CNIConfig defines the CNI provider
type CNIConfig struct {
	Provider string `yaml:"provider"`
}

// K0sConfig defines the k0s runtime parameters
type K0sConfig struct {
	Version string        `yaml:"version"`
	Storage StorageConfig `yaml:"storage"`
}

// StorageConfig defines the control-plane storage engine
type StorageConfig struct {
	Type string `yaml:"type"` // "kine" or "etcd"
}

// NodeConfig defines a microVM node
type NodeConfig struct {
	Role        NodeRole `yaml:"role"`
	Image       string   `yaml:"image,omitempty"`
	VCPUs       int      `yaml:"vcpus,omitempty"`
	MemoryMiB   int      `yaml:"memoryMiB,omitempty"`
	DiskSizeGiB int      `yaml:"diskSizeGiB,omitempty"`
}

// DefaultClusterConfig creates a baseline 1-controlplane, 1-worker cluster configuration
func DefaultClusterConfig(name string) *ClusterConfig {
	if name == "" {
		name = DefaultClusterName
	}
	return &ClusterConfig{
		Kind:       "Cluster",
		APIVersion: "fckind.io/v1alpha1",
		Name:       name,
		Networking: NetworkingConfig{
			PodSubnet:     DefaultPodSubnet,
			ServiceSubnet: DefaultSvcSubnet,
			CNI: CNIConfig{
				Provider: DefaultCNI,
			},
		},
		K0s: K0sConfig{
			Version: DefaultK0sVersion,
			Storage: StorageConfig{
				Type: "kine",
			},
		},
		Nodes: []NodeConfig{
			{
				Role:        RoleControlPlane,
				Image:       DefaultImage,
				VCPUs:       2,
				MemoryMiB:   1024,
				DiskSizeGiB: 20,
			},
			{
				Role:        RoleWorker,
				Image:       DefaultImage,
				VCPUs:       2,
				MemoryMiB:   1024,
				DiskSizeGiB: 20,
			},
		},
	}
}

// LoadConfig parses a YAML config file or returns defaults if path is empty
func LoadConfig(path string, clusterName string, nameExplicit bool) (*ClusterConfig, error) {
	if path == "" {
		cfg := DefaultClusterConfig(clusterName)
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	var cfg ClusterConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config yaml: %w", err)
	}

	if nameExplicit && clusterName != "" {
		cfg.Name = clusterName
	} else if cfg.Name == "" {
		if clusterName != "" {
			cfg.Name = clusterName
		} else {
			cfg.Name = DefaultClusterName
		}
	}

	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func (c *ClusterConfig) applyDefaults() {
	if c.Spec != nil && c.Spec.Network != nil && c.Spec.Network.KubeProxy != nil {
		if !c.Networking.KubeProxy.Disabled && c.Spec.Network.KubeProxy.Disabled {
			c.Networking.KubeProxy.Disabled = true
		}
		if c.Networking.KubeProxy.Mode == "" && c.Spec.Network.KubeProxy.Mode != "" {
			c.Networking.KubeProxy.Mode = c.Spec.Network.KubeProxy.Mode
		}
	}

	if c.Kind == "" {
		c.Kind = "Cluster"
	}
	if c.APIVersion == "" {
		c.APIVersion = "fckind.io/v1alpha1"
	}
	if c.Networking.PodSubnet == "" {
		c.Networking.PodSubnet = DefaultPodSubnet
	}
	if c.Networking.ServiceSubnet == "" {
		c.Networking.ServiceSubnet = DefaultSvcSubnet
	}
	if c.Networking.CNI.Provider == "" {
		c.Networking.CNI.Provider = DefaultCNI
	}
	if c.K0s.Version == "" {
		c.K0s.Version = DefaultK0sVersion
	}

	// Adaptive storage detection: multi control-plane -> etcd, single -> kine
	cpCount := 0
	for _, n := range c.Nodes {
		if n.Role == RoleControlPlane {
			cpCount++
		}
	}
	if c.K0s.Storage.Type == "" {
		if cpCount > 1 {
			c.K0s.Storage.Type = "etcd"
		} else {
			c.K0s.Storage.Type = "kine"
		}
	}

	for i := range c.Nodes {
		if c.Nodes[i].Image == "" {
			c.Nodes[i].Image = DefaultImage
		}
		if c.Nodes[i].VCPUs <= 0 {
			c.Nodes[i].VCPUs = 2
		}
		if c.Nodes[i].MemoryMiB <= 0 {
			c.Nodes[i].MemoryMiB = 1024
		}
		if c.Nodes[i].DiskSizeGiB <= 0 {
			c.Nodes[i].DiskSizeGiB = 20
		}
	}
}

// Validate checks the cluster configuration for semantic errors
func (c *ClusterConfig) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("cluster name cannot be empty")
	}

	cpFound := false
	for i, n := range c.Nodes {
		if n.Role != RoleControlPlane && n.Role != RoleWorker {
			return fmt.Errorf("node %d: invalid role %q (must be %q or %q)", i, n.Role, RoleControlPlane, RoleWorker)
		}
		if n.Role == RoleControlPlane {
			cpFound = true
		}
	}

	if !cpFound {
		return fmt.Errorf("cluster must contain at least one control-plane node")
	}

	return nil
}
