package main

import (
	"fmt"
	"log"
	"os"

	"github.com/creydr/ai-coworker/internal/config"
)

func main() {
	cfgPath := "config.yaml"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	fmt.Printf("ai-coworker starting with %d workers\n", cfg.Workers)
	fmt.Printf("LLM provider: %s, model: %s\n", cfg.LLM.Provider, cfg.LLM.Model)
	fmt.Printf("Sandbox runtime: %s\n", cfg.Sandbox.Runtime)
}
