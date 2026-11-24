package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
)

type fact struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	Source  string `json:"source"`
	Updated string `json:"updated"`
}

type memoryFile struct {
	Facts []fact `json:"facts"`
}

const memDir = "memory"

// memoryGuard keeps the always-in-prompt notes from outranking what was just said.
const memoryGuard = "Notes may be outdated. The current messages win. Don't mention these notes unless asked."

func loadFacts(charID string) []fact {
	raw, err := os.ReadFile(filepath.Join(memDir, charID+".json"))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Print(err)
		}
		return nil
	}

	var m memoryFile
	err = json.Unmarshal(raw, &m)
	if err != nil {
		log.Print(err)
		return nil
	}

	return m.Facts
}

func memoryPrompt(char Character) string {
	profile, err := os.ReadFile(filepath.Join(memDir, "profile.md"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Print(err)
	}

	facts := loadFacts(char.ID)

	if strings.TrimSpace(string(profile)) == "" && len(facts) == 0 {
		return ""
	}

	var memPrompt strings.Builder
	memPrompt.WriteString("\n\n<memory>\n")
	memPrompt.WriteString(memoryGuard)
	memPrompt.WriteString("\n")

	if strings.TrimSpace(string(profile)) != "" {
		memPrompt.WriteString("\n<about_user>\n")
		memPrompt.WriteString(string(profile))
		memPrompt.WriteString("\n</about_user>\n")
	}

	if len(facts) > 0 {
		memPrompt.WriteString("\n<facts>\n")
		for _, f := range facts {
			fmt.Fprintf(&memPrompt, "- [%s] (%s) %s\n", f.ID, f.Updated, f.Text)
		}
		memPrompt.WriteString("</facts>\n")
	}

	memPrompt.WriteString("\n</memory>\n")

	return memPrompt.String()
}
