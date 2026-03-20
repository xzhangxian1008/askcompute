package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"

	"lab/askplanner/internal/clinic"
	"lab/askplanner/internal/codex"
	"lab/askplanner/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	logFile, err := config.SetupLogging(cfg.LogFile)
	if err != nil {
		log.Fatalf("setup logging: %v", err)
	}
	defer logFile.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	responder, err := codex.NewResponder(cfg)
	if err != nil {
		log.Fatalf("build codex responder: %v", err)
	}
	prefetcher := clinic.NewPrefetcher(cfg)

	fmt.Printf("askplanner v2 (backend: codex-cli, model: %s)\n", cfg.CodexModel)
	fmt.Println("Type your question, or 'quit' to exit. Use 'reset' to start a new session.")
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)
	const conversationKey = "cli:default"
	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}

		question := strings.TrimSpace(scanner.Text())
		if question == "" {
			continue
		}
		if question == "quit" || question == "exit" {
			fmt.Println("Bye!")
			break
		}
		if question == "reset" {
			if err := responder.Reset(conversationKey); err != nil {
				fmt.Printf("Error: %v\n\n", err)
			} else {
				fmt.Println("Session reset.")
				fmt.Println()
			}
			continue
		}

		fmt.Println()
		runtimeCtx, err := prefetcher.Enrich(ctx, question, codex.RuntimeContext{})
		if err != nil {
			if msg := clinic.UserFacingMessage(err); msg != "" {
				fmt.Printf("%s\n\n", msg)
				continue
			}
			fmt.Printf("Error: %v\n\n", err)
			continue
		}

		answer, err := responder.AnswerWithContext(ctx, conversationKey, question, runtimeCtx)
		if err != nil {
			fmt.Printf("Error: %v\n\n", err)
			continue
		}

		fmt.Println()
		fmt.Println(answer)
		fmt.Println()
	}
}
