package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/usewhale/whale/internal/core"
	whalemcp "github.com/usewhale/whale/internal/mcp"
	"github.com/usewhale/whale/internal/tools"
)

func main() {
	ts, err := tools.NewToolset("/tmp")
	if err != nil {
		panic(err)
	}

	cfg, err := whalemcp.LoadConfig("/Users/goranka/.whale/mcp.json")
	if err != nil {
		cfg, err = whalemcp.LoadConfig("/Users/goranka/.whale/mcp.json.bak")
		if err != nil {
			panic(fmt.Sprintf("no mcp config: %v", err))
		}
	}
	fmt.Println("Connecting to MCP servers...")
	mgr := whalemcp.NewManager(cfg, "/tmp")
	mgr.Initialize(context.Background())
	defer mgr.Close()
	time.Sleep(8 * time.Second)

	catalog := mgr.BuildDeferredCatalog()
	if catalog.Empty() {
		fmt.Println("No MCP tools available — server may have failed to connect")
		return
	}

	// OLD: eager
	mcpFull := mgr.Tools()
	oldTools := append([]core.Tool{}, ts.Tools()...)
	oldTools = append(oldTools, mcpFull...)
	oldPayload := toolsPayload(oldTools)
	oldBytes := len(oldPayload)

	// NEW: deferred
	newTools := ts.Tools()
	newPayload := toolsPayload(newTools)
	newBytes := len(newPayload)
	availableBlock := whalemcp.RenderAvailableDeferredTools(catalog)
	blockBytes := len(availableBlock)

	fmt.Println()
	fmt.Println("============================================================")
	fmt.Println("  MCP Tool Loading: Eager vs Deferred")
	fmt.Println("============================================================")
	fmt.Printf("  MCP server: %d tool(s)\n\n", len(catalog.Names()))

	fmt.Printf("  EAGER (old) — all MCP schemas in provider payload\n")
	fmt.Printf("    Base tools:     %d  (%d KB)\n", len(ts.Tools()), len(toolsPayload(ts.Tools()))/1024)
	fmt.Printf("    MCP tools:      %d  (%d KB, full schemas)\n", len(mcpFull), len(toolsPayload(mcpFull))/1024)
	fmt.Printf("    Total payload:  %d bytes  ≈ %d tokens\n\n", oldBytes, oldBytes/4)

	fmt.Printf("  DEFERRED (new) — only tool_search + name block\n")
	fmt.Printf("    Base+t_search:  %d  (%d KB)\n", len(newTools), newBytes/1024)
	fmt.Printf("    Available blk:  %d bytes  ≈ %d tokens (names only)\n", blockBytes, blockBytes/4)
	combined := newBytes + blockBytes
	fmt.Printf("    Total combined: %d bytes  ≈ %d tokens\n\n", combined, combined/4)

	savedBytes := oldBytes - combined
	savedPct := float64(savedBytes) / float64(oldBytes) * 100
	fmt.Printf("  SAVINGS: %d bytes (%.1f%%) ≈ %d tokens/turn\n\n", savedBytes, savedPct, savedBytes/4)

	// Promote cost
	if len(mcpFull) > 0 {
		single := toolsPayload([]core.Tool{mcpFull[0]})
		fmt.Printf("  PROMOTE: loading 1 tool adds %d bytes to response (one-time)\n", len(single))
		fmt.Printf("  Break-even: after %d turns of using <= N tools vs eager\n", len(single)/(savedBytes/len(mcpFull))+1)
	}
	fmt.Println("============================================================")
}

func toolsPayload(tools []core.Tool) []byte {
	payloads := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		payloads = append(payloads, core.ProviderToolPayload(t))
	}
	b, _ := json.Marshal(payloads)
	return b
}
