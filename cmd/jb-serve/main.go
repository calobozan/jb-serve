package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/calobozan/jb-serve/internal/client"
	"github.com/calobozan/jb-serve/internal/config"
	"github.com/calobozan/jb-serve/internal/server"
	"github.com/calobozan/jb-serve/internal/tools"
	"github.com/spf13/cobra"
)

var (
	cfg      *config.Config
	manager  *tools.Manager
	executor *tools.Executor

	// Global flags
	serverPort int
	apiClient  *client.Client
)

var rootCmd = &cobra.Command{
	Use:   "jb-serve",
	Short: "Jumpboot Tool Server - Run Python tools via RPC",
	Long: `jb-serve manages and serves Python tools using jumpboot environments.
Each tool is a git repo with a jumpboot.yaml manifest describing its
capabilities, dependencies, and RPC interface.

Uses github.com/richinsley/jumpboot for Python environment management.

Most commands communicate with a running jb-serve server (like ollama).
Start the server with: jb-serve serve

Commands that require the server: list, info, start, stop, call
Commands that work standalone: install, serve`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if cmd.Name() == "help" {
			return nil
		}
		return initApp(cmd)
	},
}

func initApp(cmd *cobra.Command) error {
	// Commands that need direct manager access (standalone)
	standaloneCommands := map[string]bool{
		"install": true,
		"serve":   true,
	}

	// For standalone commands, initialize manager directly
	if standaloneCommands[cmd.Name()] {
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

	// For all other commands, use HTTP client
	apiClient = client.NewFromPort(serverPort)
	if err := apiClient.Ping(); err != nil {
		return fmt.Errorf("cannot connect to jb-serve on port %d: %w\n\nIs the server running? Start it with: jb-serve serve", serverPort, err)
	}

	return nil
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	// Global port flag
	rootCmd.PersistentFlags().IntVarP(&serverPort, "port", "p", 9800, "Server port to connect to")

	rootCmd.AddCommand(installCmd)
	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(infoCmd)
	rootCmd.AddCommand(schemaCmd)
	rootCmd.AddCommand(callCmd)
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(stopCmd)
	rootCmd.AddCommand(serveCmd)
}

// install - standalone, doesn't need server
var installCmd = &cobra.Command{
	Use:   "install <git-url-or-path>",
	Short: "Install a tool from git or local path",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, err := manager.Install(args[0])
		return err
	},
}

// list - uses HTTP client
var listJSON bool
var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed tools",
	RunE: func(cmd *cobra.Command, args []string) error {
		tools, err := apiClient.List()
		if err != nil {
			return err
		}

		if listJSON {
			data, _ := json.MarshalIndent(tools, "", "  ")
			fmt.Println(string(data))
			return nil
		}

		if len(tools) == 0 {
			fmt.Println("No tools installed.")
			return nil
		}

		fmt.Printf("%-20s %-10s %-12s %s\n", "NAME", "VERSION", "MODE", "STATUS")
		for _, t := range tools {
			fmt.Printf("%-20s %-10s %-12s %s\n",
				t.Name, t.Version, t.Mode, t.Status)
		}
		return nil
	},
}

func init() {
	listCmd.Flags().BoolVar(&listJSON, "json", false, "Output as JSON")
}

// info - uses HTTP client
var infoCmd = &cobra.Command{
	Use:   "info <tool-name>",
	Short: "Show tool details",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		info, err := apiClient.Info(args[0])
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

		if names := info.MethodNames(); len(names) > 0 {
			fmt.Println("\nMethods:")
			for _, name := range names {
				fmt.Printf("  - %s\n", name)
			}
		}

		return nil
	},
}

// schema - uses HTTP client (gets info then extracts schema)
var schemaCmd = &cobra.Command{
	Use:   "schema <tool-name>[.method]",
	Short: "Show RPC schema",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		parts := strings.SplitN(args[0], ".", 2)
		toolName := parts[0]

		info, err := apiClient.Info(toolName)
		if err != nil {
			return err
		}

		// Methods from /v1/tools/{name} is a map[string]interface{}
		methods, ok := info.Methods.(map[string]interface{})
		if !ok {
			return fmt.Errorf("could not parse methods schema")
		}

		var data interface{}
		if len(parts) == 2 {
			method, ok := methods[parts[1]]
			if !ok {
				return fmt.Errorf("method not found: %s", parts[1])
			}
			data = method
		} else {
			data = methods
		}

		out, _ := json.MarshalIndent(data, "", "  ")
		fmt.Println(string(out))
		return nil
	},
}

// call - uses HTTP client
var callJSON string
var callCmd = &cobra.Command{
	Use:   "call <tool.method> [key=value ...]",
	Short: "Call a tool method",
	Long: `Call a tool method with parameters.

Parameters can be passed as key=value pairs:
  jb-serve call calculator.add a=2 b=3

Or as JSON with --json:
  jb-serve call calculator.add --json '{"a": 2, "b": 3}'

Requires the jb-serve server to be running.`,
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		parts := strings.SplitN(args[0], ".", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid format, expected tool.method")
		}

		toolName := parts[0]
		methodName := parts[1]

		// Parse parameters
		params := make(map[string]interface{})

		if callJSON != "" {
			// JSON input mode
			if err := json.Unmarshal([]byte(callJSON), &params); err != nil {
				return fmt.Errorf("invalid JSON: %w", err)
			}
		} else {
			// key=value pairs from remaining args
			// Note: Without schema access, we treat everything as strings
			// The server will handle type conversion based on method schema
			for _, arg := range args[1:] {
				kv := strings.SplitN(arg, "=", 2)
				if len(kv) != 2 {
					return fmt.Errorf("invalid parameter format %q, expected key=value", arg)
				}
				key, val := kv[0], kv[1]

				// Try to parse as number or boolean
				var parsed interface{} = val
				var num float64
				if _, err := fmt.Sscanf(val, "%f", &num); err == nil {
					parsed = num
				} else if val == "true" {
					parsed = true
				} else if val == "false" {
					parsed = false
				}
				params[key] = parsed
			}
		}

		result, err := apiClient.Call(toolName, methodName, params)
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

// start - uses HTTP client
var startCmd = &cobra.Command{
	Use:   "start <tool-name>",
	Short: "Start a persistent tool",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		status, err := apiClient.Start(args[0])
		if err != nil {
			return err
		}
		fmt.Printf("Started %s\n", status.Tool)
		return nil
	},
}

// stop - uses HTTP client
var stopCmd = &cobra.Command{
	Use:   "stop <tool-name>",
	Short: "Stop a persistent tool",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		status, err := apiClient.Stop(args[0])
		if err != nil {
			return err
		}
		fmt.Printf("Stopped %s\n", status.Tool)
		return nil
	},
}

// files - file store management
var filesCmd = &cobra.Command{
	Use:   "files",
	Short: "Manage file store",
}

var filesListCmd = &cobra.Command{
	Use:   "ls",
	Short: "List files in store",
	RunE: func(cmd *cobra.Command, args []string) error {
		files, err := apiClient.FilesList()
		if err != nil {
			return err
		}

		if len(files) == 0 {
			fmt.Println("No files in store.")
			return nil
		}

		fmt.Printf("%-36s  %-10s  %-20s  %s\n", "ID", "SIZE", "CREATED", "NAME")
		for _, f := range files {
			fmt.Printf("%-36s  %-10d  %-20d  %s\n",
				f["id"], int64(f["size"].(float64)), int64(f["created_at"].(float64)), f["name"])
		}
		return nil
	},
}

var filesImportName string
var filesImportTTL int64
var filesImportCmd = &cobra.Command{
	Use:   "import <path>",
	Short: "Import a file into store",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		info, err := apiClient.FilesImport(args[0], filesImportName, filesImportTTL)
		if err != nil {
			return err
		}
		out, _ := json.MarshalIndent(info, "", "  ")
		fmt.Println(string(out))
		return nil
	},
}

var filesInfoCmd = &cobra.Command{
	Use:   "info <id>",
	Short: "Get file info",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		info, err := apiClient.FilesInfo(args[0])
		if err != nil {
			return err
		}
		out, _ := json.MarshalIndent(info, "", "  ")
		fmt.Println(string(out))
		return nil
	},
}

var filesDeleteCmd = &cobra.Command{
	Use:   "rm <id>",
	Short: "Delete a file",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := apiClient.FilesDelete(args[0]); err != nil {
			return err
		}
		fmt.Println("Deleted")
		return nil
	},
}

func init() {
	filesImportCmd.Flags().StringVar(&filesImportName, "name", "", "Display name for file")
	filesImportCmd.Flags().Int64Var(&filesImportTTL, "ttl", 0, "TTL in seconds (0 = permanent)")

	filesCmd.AddCommand(filesListCmd)
	filesCmd.AddCommand(filesImportCmd)
	filesCmd.AddCommand(filesInfoCmd)
	filesCmd.AddCommand(filesDeleteCmd)
	rootCmd.AddCommand(filesCmd)
}

// serve - standalone, starts the server
var (
	servePort         int
	serveStorePath    string
	serveStoreDisable bool
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the HTTP API server",
	RunE: func(cmd *cobra.Command, args []string) error {
		opts := server.Options{
			FileStorePath:    serveStorePath,
			FileStoreDisable: serveStoreDisable,
		}
		srv := server.NewWithOptions(cfg, manager, executor, opts)
		return srv.ListenAndServe(servePort)
	},
}

func init() {
	serveCmd.Flags().IntVar(&servePort, "port", 9800, "Port to listen on")
	serveCmd.Flags().StringVar(&serveStorePath, "store-path", "", "File store directory (default: ~/.jb-serve)")
	serveCmd.Flags().BoolVar(&serveStoreDisable, "no-store", false, "Disable file store")
}
