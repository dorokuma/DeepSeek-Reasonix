package resource

import "fmt"

type ResourceBudget struct {
	MaxTokens      int `json:"max_tokens"`
	MaxToolCalls   int `json:"max_tool_calls"`
	MaxMemoryNodes int `json:"max_memory_nodes"`
}

type Usage struct {
	Tokens      int `json:"tokens"`
	ToolCalls   int `json:"tool_calls"`
	MemoryNodes int `json:"memory_nodes"`
}

type Decision struct {
	Allowed bool     `json:"allowed"`
	Reasons []string `json:"reasons,omitempty"`
	Budget  ResourceBudget
	Usage   Usage
}

func DefaultBudget() ResourceBudget {
	return ResourceBudget{
		MaxTokens:      32000,
		MaxToolCalls:   20,
		MaxMemoryNodes: 300,
	}
}

func Enforce(budget ResourceBudget, usage Usage) Decision {
	budget = Normalize(budget)
	decision := Decision{Allowed: true, Budget: budget, Usage: usage}
	if usage.Tokens > budget.MaxTokens {
		decision.Allowed = false
		decision.Reasons = append(decision.Reasons, fmt.Sprintf("token budget exceeded (%d>%d)", usage.Tokens, budget.MaxTokens))
	}
	if usage.ToolCalls > budget.MaxToolCalls {
		decision.Allowed = false
		decision.Reasons = append(decision.Reasons, fmt.Sprintf("tool call budget exceeded (%d>%d)", usage.ToolCalls, budget.MaxToolCalls))
	}
	if usage.MemoryNodes > budget.MaxMemoryNodes {
		decision.Allowed = false
		decision.Reasons = append(decision.Reasons, fmt.Sprintf("memory node budget exceeded (%d>%d)", usage.MemoryNodes, budget.MaxMemoryNodes))
	}
	return decision
}

func Normalize(budget ResourceBudget) ResourceBudget {
	def := DefaultBudget()
	if budget.MaxTokens <= 0 {
		budget.MaxTokens = def.MaxTokens
	}
	if budget.MaxToolCalls <= 0 {
		budget.MaxToolCalls = def.MaxToolCalls
	}
	if budget.MaxMemoryNodes <= 0 {
		budget.MaxMemoryNodes = def.MaxMemoryNodes
	}
	return budget
}

func ScaleForCanary(budget ResourceBudget, percent int) ResourceBudget {
	budget = Normalize(budget)
	if percent <= 0 {
		percent = 1
	}
	if percent > 100 {
		percent = 100
	}
	scale := func(v int) int {
		next := v * percent / 100
		if next < 1 {
			return 1
		}
		return next
	}
	return ResourceBudget{
		MaxTokens:      scale(budget.MaxTokens),
		MaxToolCalls:   scale(budget.MaxToolCalls),
		MaxMemoryNodes: budget.MaxMemoryNodes,
	}
}
