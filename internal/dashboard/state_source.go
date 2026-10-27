package dashboard

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"code.sarnas.ru/api-gateway/internal/config"
)

// DiscoverySummary is the last-known-good discovery state persisted by the
// gateway. The raw JSON uses Go field names, so it is decoded into
// config.TargetConfig/config.RoutingRule and then mapped to secret-free views.
type DiscoverySummary struct {
	Loaded    bool         `json:"loaded"`
	StateFile string       `json:"state_file"`
	ModTime   time.Time    `json:"mod_time"`
	Targets   []TargetView `json:"targets"`
	Rules     []RuleView   `json:"rules"`
}

// LoadDiscoverySummary reads the gateway discovery state file at path.
func LoadDiscoverySummary(path string) (*DiscoverySummary, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read discovery state: %w", err)
	}
	var state struct {
		Targets []config.TargetConfig `json:"Targets"`
		Rules   []config.RoutingRule  `json:"Rules"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode discovery state: %w", err)
	}
	var modTime time.Time
	if info, err := os.Stat(path); err == nil {
		modTime = info.ModTime()
	}
	return &DiscoverySummary{
		Loaded:    true,
		StateFile: path,
		ModTime:   modTime,
		Targets:   viewTargets(state.Targets),
		Rules:     viewRules(state.Rules),
	}, nil
}
