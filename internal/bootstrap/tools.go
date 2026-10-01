package bootstrap

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/shadowsafin/corerouter/internal/domain"
	"github.com/shadowsafin/corerouter/internal/storage"
	"github.com/shadowsafin/corerouter/internal/tools"
)

// SeedBuiltinTools registers the gateway's built-in tools.
//
// The seed runs on every start and is idempotent. It deliberately writes the
// minimal correct definition rather than overwriting everything: an operator
// who disabled a tool in the dashboard keeps it disabled, because a restart must
// not quietly re-enable a capability someone turned off.
func SeedBuiltinTools(
	ctx context.Context,
	registry *storage.ToolRepository,
	logger *slog.Logger,
) error {
	if registry == nil {
		return nil
	}

	nowSchema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"format": {
				"type": "string",
				"description": "Optional Go time layout, e.g. 2006-01-02. Defaults to RFC3339."
			}
		},
		"additionalProperties": false
	}`)
	echoSchema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"value": {"type": "string", "description": "The value to echo back."}
		},
		"required": ["value"],
		"additionalProperties": false
	}`)

	seeds := []domain.ToolSpec{
		{
			Name:        "now",
			Description: "Return the gateway's current UTC time. Useful for grounding time-sensitive answers.",
			Kind:        domain.ToolBuiltin,
			Owner:       domain.OwnerPlatform,
			Parameters:  nowSchema,
			SafetyLevel: domain.SafetySafe,
			Executable:  true,
			Handler:     "now",
			Version:     "1",
			Enabled:     true,
			Labels:      map[string]string{"managed-by": "bootstrap"},
		},
		{
			Name:        "echo",
			Description: "Return the value it is given. Useful for verifying a multi-step tool flow end to end.",
			Kind:        domain.ToolBuiltin,
			Owner:       domain.OwnerPlatform,
			Parameters:  echoSchema,
			SafetyLevel: domain.SafetySafe,
			Executable:  true,
			Handler:     "echo",
			Version:     "1",
			Enabled:     true,
			Labels:      map[string]string{"managed-by": "bootstrap"},
		},
	}

	for _, seed := range seeds {
		if !tools.HandlerKnown(seed.Handler) {
			continue
		}
		existing, err := registry.List(ctx)
		if err != nil {
			return err
		}
		if existingTool, ok := findToolByName(existing, seed.Name); ok {
			// Preserve an operator decision about the enabled flag.
			seed.Enabled = existingTool.Enabled
			seed.ID = existingTool.ID
		}
		if _, err := registry.Upsert(ctx, &seed); err != nil {
			return err
		}
	}

	logger.Info("seeded the built-in tool registry",
		"tools", len(seeds),
		"executors", tools.HandlerNames)
	return nil
}

// findToolByName locates a platform tool by name.
func findToolByName(all []domain.ToolSpec, name string) (domain.ToolSpec, bool) {
	for _, spec := range all {
		if spec.Name == name && spec.TenantID == "" {
			return spec, true
		}
	}
	return domain.ToolSpec{}, false
}
