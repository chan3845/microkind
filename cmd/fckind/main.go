package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"fckind/pkg/cluster"
	"fckind/pkg/config"
	"fckind/pkg/image"
	"fckind/pkg/vsock"

	"github.com/spf13/cobra"
)

var (
	clusterName          string
	configFile           string
	nodeImage            string
	k0sVersion           string
	isSlim               bool
	exportKubeconfigPath string
)

func main() {
	rootCmd := &cobra.Command{
		Use:     "microkind",
		Aliases: []string{"fckind"},
		Short:   "microkind is kind for Firecracker microVMs (powered by Alpine + k0s)",
		Long:    "A fast, ultra-minimal, kind-compatible Kubernetes cluster orchestrator running upstream k0s inside Firecracker microVMs on Alpine Linux.",
	}

	rootCmd.PersistentFlags().StringVarP(&clusterName, "name", "n", config.DefaultClusterName, "Cluster name")

	// 1. 'create' command group
	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Creates one of [cluster]",
	}

	createClusterCmd := &cobra.Command{
		Use:   "cluster",
		Short: "Creates a local Kubernetes cluster inside Firecracker microVMs",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			nameExplicit := cmd.Flags().Changed("name")
			cfg, err := config.LoadConfig(configFile, clusterName, nameExplicit)
			if err != nil {
				return err
			}
			if nodeImage != "" {
				for i := range cfg.Nodes {
					cfg.Nodes[i].Image = nodeImage
				}
			}

			mgr, err := cluster.NewManager()
			if err != nil {
				return err
			}
			return mgr.CreateCluster(ctx, cfg)
		},
	}
	createClusterCmd.Flags().StringVarP(&configFile, "config", "c", "", "Path to cluster config file")
	createClusterCmd.Flags().StringVar(&nodeImage, "image", "", "Custom node image to use")
	createCmd.AddCommand(createClusterCmd)

	// 2. 'delete' command group
	deleteCmd := &cobra.Command{
		Use:   "delete",
		Short: "Deletes one of [cluster]",
	}

	deleteClusterCmd := &cobra.Command{
		Use:   "cluster",
		Short: "Deletes a cluster and its microVMs",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			mgr, err := cluster.NewManager()
			if err != nil {
				return err
			}
			return mgr.DeleteCluster(ctx, clusterName)
		},
	}
	deleteCmd.AddCommand(deleteClusterCmd)

	// 3. 'get' command group
	getCmd := &cobra.Command{
		Use:   "get",
		Short: "Gets one of [clusters, nodes]",
	}

	getClustersCmd := &cobra.Command{
		Use:   "clusters",
		Short: "Lists existing clusters",
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := cluster.NewManager()
			if err != nil {
				return err
			}
			clusters, err := mgr.ListClusters()
			if err != nil {
				return err
			}
			if len(clusters) == 0 {
				fmt.Println("No clusters found.")
				return nil
			}
			for _, c := range clusters {
				fmt.Println(c)
			}
			return nil
		},
	}

	getNodesCmd := &cobra.Command{
		Use:   "nodes",
		Short: "Lists nodes for a cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := cluster.NewManager()
			if err != nil {
				return err
			}
			state, err := mgr.GetCluster(clusterName)
			if err != nil {
				return err
			}
			for _, n := range state.Nodes {
				fmt.Printf("%-25s %-15s %s\n", n.Name, n.Role, n.GuestIP)
			}
			return nil
		},
	}

	getKubeconfigCmd := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Prints the kubeconfig for a cluster to stdout",
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := cluster.NewManager()
			if err != nil {
				return err
			}
			kubeconfigPath := filepath.Join(mgr.ClusterDir(clusterName), "kubeconfig")
			data, err := os.ReadFile(kubeconfigPath)
			if err != nil {
				return fmt.Errorf("read kubeconfig for cluster %q: %w", clusterName, err)
			}
			fmt.Print(string(data))
			return nil
		},
	}
	getCmd.AddCommand(getClustersCmd, getNodesCmd, getKubeconfigCmd)

	// 4. 'export' command group
	exportCmd := &cobra.Command{
		Use:   "export",
		Short: "Exports cluster resources",
	}

	exportKubeconfigCmd := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Exports cluster kubeconfig to a specified file or ~/.kube/config",
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := cluster.NewManager()
			if err != nil {
				return err
			}
			srcPath := filepath.Join(mgr.ClusterDir(clusterName), "kubeconfig")
			data, err := os.ReadFile(srcPath)
			if err != nil {
				return fmt.Errorf("read kubeconfig for cluster %q: %w", clusterName, err)
			}
			targetPath := exportKubeconfigPath
			if targetPath == "" {
				home, _ := os.UserHomeDir()
				targetPath = filepath.Join(home, ".kube", "config")
			}
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return fmt.Errorf("create kube dir: %w", err)
			}
			if err := os.WriteFile(targetPath, data, 0600); err != nil {
				return fmt.Errorf("write kubeconfig to %s: %w", targetPath, err)
			}
			fmt.Printf("Kubeconfig exported to %s\n", targetPath)
			return nil
		},
	}
	exportKubeconfigCmd.Flags().StringVar(&exportKubeconfigPath, "kubeconfig", "", "Path to export kubeconfig (defaults to ~/.kube/config)")
	exportCmd.AddCommand(exportKubeconfigCmd)

	// 4. 'load' command group
	loadCmd := &cobra.Command{
		Use:   "load",
		Short: "Loads an image into nodes",
	}

	loadDockerImageCmd := &cobra.Command{
		Use:   "docker-image [image-name]",
		Short: "Loads a Docker image from the host into all cluster microVMs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			mgr, err := cluster.NewManager()
			if err != nil {
				return err
			}
			return mgr.LoadDockerImage(ctx, clusterName, args[0])
		},
	}
	loadCmd.AddCommand(loadDockerImageCmd)

	// 5. 'build' command group
	buildCmd := &cobra.Command{
		Use:   "build",
		Short: "Builds one of [node-image]",
	}

	buildNodeImageCmd := &cobra.Command{
		Use:   "node-image",
		Short: "Builds a versioned Alpine+k0s Firecracker node image",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			airgap := !isSlim
			builder, err := image.NewBuilder(k0sVersion, airgap)
			if err != nil {
				return err
			}
			repoRoot, _ := os.Getwd()
			return builder.BuildNodeImage(ctx, repoRoot)
		},
	}
	buildNodeImageCmd.Flags().StringVar(&k0sVersion, "version", config.DefaultK0sVersion, "k0s Kubernetes version tag")
	buildNodeImageCmd.Flags().BoolVar(&isSlim, "slim", false, "Do not include the pre-baked airgap bundle")
	buildCmd.AddCommand(buildNodeImageCmd)

	// 6. 'exec' command (Zero-SSH vsock exec into any node)
	execCmd := &cobra.Command{
		Use:   "exec [node-name] -- [command...]",
		Short: "Execute a command inside a cluster microVM via virtio-vsock",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			targetNode := args[0]
			command := args[1:]

			mgr, err := cluster.NewManager()
			if err != nil {
				return err
			}
			state, err := mgr.GetCluster(clusterName)
			if err != nil {
				return err
			}

			nodeState, ok := state.Nodes[targetNode]
			if !ok {
				return fmt.Errorf("node %q not found in cluster %q", targetNode, clusterName)
			}

			cli := vsock.NewClient(nodeState.VsockPath)
			resp, err := cli.Exec(ctx, command, nil, "", nil)
			if err != nil {
				return fmt.Errorf("exec error: %w", err)
			}

			os.Stdout.Write(resp.Stdout)
			os.Stderr.Write(resp.Stderr)
			os.Exit(resp.ExitCode)
			return nil
		},
	}

	rootCmd.AddCommand(createCmd, deleteCmd, getCmd, exportCmd, loadCmd, buildCmd, execCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
