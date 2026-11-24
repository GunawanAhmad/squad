package main

import (
	"os"
	"sync"

	"characters/llm"
)

// Created lazily so .env is loaded first.
var openCodeModel = sync.OnceValue(func() llm.Model {
	return llm.NewOpenCode(
		os.Getenv("OPENCODE_API_KEY"),
		envOr("OPENCODE_BASE_URL", "https://opencode.ai/inference/go/openai/v1"),
	)
})

var claudeModel = sync.OnceValue(func() llm.Model { return llm.NewClaude() })
