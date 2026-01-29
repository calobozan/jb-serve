package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/calobozan/jb-serve/internal/config"
	"github.com/calobozan/jb-serve/internal/server"
	"github.com/calobozan/jb-serve/internal/tools"
	"github.com/spf13/cobra"
)

var (
	cfg      *config.Config
	manager  *tools.Manager
	executor *tools.Executor
)

var rootCmd = &cobra.Command{
	Use:   "jb-serve",
	Short: "Jumpboot Tool Server - Run Python tools via RPC",
	Long: `jb-serve manages and serves Python tools using jumpboot environments.
Each tool is a git repo with a jumpboot.yaml manifest describing its
capabilities, dependencies, and RPC interface.

Uses github.com/richinsley/jumpboot for Python environment management.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if cmd.Name() == "help" {
			return nil
		}
		return initApp()
	},
}

func initApp() error {
	var err error
	cfg, err = config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if err := cfg.EnsureDirs(); err != nil {
		return fmt.Errorf("failed to create directories: %w", err)
	}

	manager = tools.NewManager(cfg)
	if err := manager.LoadAll(); err != nil {
		return fmt.Errorf("failed to load tools: %w", err)
	}

	executor = tools.NewExecutor(manager)
	return nil
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(installCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(infoCmd)
	rootCmd.AddCommand(schemaCmd)
	rootCmd.AddCommand(callCmd)
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(serveCmd)
}

// install
var installCmd = &cobra.Command{
	Use:   "install <git-url-or-path>",
	Short: "Install a tool from git or local path",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, err := manager.Install(args[0])
		return err
	},
}

// list
var listJSON bool
var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed tools",
	RunE: func(cmd *cobra.Command, args []string) error {
		toolList := manager.List()

		if listJSON {
			data, _ := json.MarshalIndent(toolList, "", "  ")
			fmt.Println(string(data))
			return nil
		}

		if len(toolList) == 0 {
			fmt.Println("No tools installed.")
			return nil
		}

		fmt.Printf("%-20s %-10s %-12s %s\n", "NAME", "VERSION", "MODE", "STATUS")
		for _, t := range toolList {
			fmt.Printf("%-20s %-10s %-12s %s\n",
				t.Name, t.Manifest.Version, t.Manifest.Runtime.Mode, t.Status)
		}
		return nil
	},
}

func init() {
	listCmd.Flags().BoolVar(&listJSON, "json", false, "Output as JSON")
}

// info
var infoCmd = &cobra.Command{
	Use:   "info <tool-name>",
	Short: "Show tool details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		info, err := manager.Info(args[0])
		if err != nil {
			return err
		}

		fmt.Printf("Name:         %s\n", info.Name)
		fmt.Printf("Version:      %s\n", info.Version)
		fmt.Printf("Description:  %s\n", strings.TrimSpace(info.Description))
		fmt.Printf("Mode:         %s\n", info.Mode)
		fmt.Printf("Status:       %s\n", info.Status)

		if len(info.Capabilities) > 0 {
			fmt.Println("\nCapabilities:")
			for _, cap := range info.Capabilities {
				fmt.Printf("  - %s\n", cap)
			}
		}

		fmt.Println("\nMethods:")
		for name, method := range info.Methods {
			fmt.Printf("  %s: %s\n", name, method.Description)
		}

		return nil
	},
}

// schema
var schemaCmd = &cobra.Command{
	Use:   "schema <tool-name>[.method]",
	Short: "Show RPC schema",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		parts := strings.SplitN(args[0], ".", 2)
		toolName := parts[0]

		tool, ok := manager.Get(toolName)
		if !ok {
			return fmt.Errorf("tool not found: %s", toolName)
		}

		var data interface{}
		if len(parts) == 2 {
			method, ok := tool.Manifest.RPC.Methods[parts[1]]
			if !ok {
				return fmt.Errorf("method not found: %s", parts[1])
			}
			data = method
		} else {
			data = tool.Manifest.RPC.Methods
		}

		out, _ := json.MarshalIndent(data, "", "  ")
		fmt.Println(string(out))
		return nil
	},
}

// call
var callJSON string
var callCmd = &cobra.Command{
	Use:   "call <tool.method> [key=value ...]",
	Short: "Call a tool method",
	Long: `Call a tool method with parameters.

Parameters can be passed as key=value pairs:
  jb-serve call calculator.add a=2 b=3

Or as JSON with --json:
  jb-serve call calculator.add --json '{"a": 2, "b": 3}'

Values are automatically converted based on the method's schema.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		parts := strings.SplitN(args[0], ".", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid format, expected tool.method")
		}

		toolName := parts[0]
		methodName := parts[1]

		tool, ok := manager.Get(toolName)
		if !ok {
			return fmt.Errorf("tool not found: %s", toolName)
		}

		method, ok := tool.Manifest.RPC.Methods[methodName]
		if !ok {
			return fmt.Errorf("method not found: %s", methodName)
		}

		// Parse parameters
		params := make(map[string]interface{})

		if callJSON != "" {
			// JSON input mode
			if err := json.Unmarshal([]byte(callJSON), &params); err != nil {
				return fmt.Errorf("invalid JSON: %w", err)
			}
		} else {
			// key=value pairs from remaining args
			for _, arg := range args[1:] {
				kv := strings.SplitN(arg, "=", 2)
				if len(kv) != 2 {
					return fmt.Errorf("invalid parameter format %q, expected key=value", arg)
				}
				key, val := kv[0], kv[1]

				// Type conversion based on schema
				expectedType := "string"
				if method.Input != nil && method.Input.Properties != nil {
					if prop, ok := method.Input.Properties[key]; ok && prop != nil {
						expectedType = prop.Type
					}
				}

				switch expectedType {
				case "number", "integer":
					var num float64
					if _, err := fmt.Sscanf(val, "%f", &num); err == nil {
						params[key] = num
					} else {
						params[key] = val
					}
				case "boolean":
					params[key] = val == "true" || val == "1"
				default:
					params[key] = val
				}
			}
		}

		result, err := executor.Call(toolName, methodName, params)
		if err != nil {
			return err
		}

		out, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(out))
		return nil
	},
}

func init() {
	callCmd.Flags().StringVar(&callJSON, "json", "", "Parameters as JSON object")
}

// start
var startCmd = &cobra.Command{
	Use:   "start <tool-name>",
	Short: "Start a persistent tool",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return executor.Start(args[0])
	},
}

// stop
var stopCmd = &cobra.Command{
	Use:   "stop <tool-name>",
	Short: "Stop a persistent tool",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return executor.Stop(args[0])
	},
}

// serve
var servePort int
var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the HTTP API server",
	RunE: func(cmd *cobra.Command, args []string) error {
		srv := server.New(cfg, manager, executor)
		return srv.ListenAndServe(servePort)
	},
}

func init() {
	serveCmd.Flags().IntVarP(&servePort, "port", "p", 9800, "Port to listen on")
}
