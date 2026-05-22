package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"windnssyncagent/internal/agent"
	"windnssyncagent/internal/config"
	"windnssyncagent/internal/dns"
	"windnssyncagent/internal/syncer"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "agent":
		runAgent(os.Args[2:])
	case "sync":
		runSync(os.Args[2:])
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(2)
	}
}

func runAgent(args []string) {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	configPath := fs.String("config", "config/agent.json", "agent config file path")
	_ = fs.Parse(args)

	cfg, err := config.LoadAgent(*configPath)
	if err != nil {
		log.Fatalf("load agent config: %v", err)
	}

	provider := dns.NewPowerShellProvider()
	server := agent.NewServer(cfg, provider)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := server.ListenAndServe(ctx); err != nil {
		log.Fatalf("agent stopped: %v", err)
	}
}

func runSync(args []string) {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	configPath := fs.String("config", "config/sync.json", "sync config file path")
	_ = fs.Parse(args)

	cfg, err := config.LoadSync(*configPath)
	if err != nil {
		log.Fatalf("load sync config: %v", err)
	}

	ctx := context.Background()
	result, err := syncer.RunWithLogger(ctx, cfg, func(message string) {
		fmt.Println(message)
	})
	if err != nil {
		log.Fatalf("sync failed: %v", err)
	}

	printSyncResult(result)
}

func printSyncResult(result syncer.Result) {
	fmt.Printf("dryRun=%v\n", result.DryRun)
	fmt.Printf("zonesCreated=%d zonesDeleted=%d recordsAdded=%d recordsUpdated=%d recordsDeleted=%d recordsRewritten=%d\n",
		len(result.ZonesCreated), len(result.ZonesDeleted), len(result.RecordsAdded), len(result.RecordsUpdated), len(result.RecordsDeleted), len(result.RecordsRewritten))
}

func printUsage() {
	fmt.Println(`WinDnsSyncAgent

Usage:
  windnssyncagent agent -config config/agent.json
  windnssyncagent sync  -config config/sync.json`)
}
