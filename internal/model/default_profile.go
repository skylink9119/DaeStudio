package model

import (
	"embed"
	"encoding/json"
	"time"
)

//go:embed default_profile.json
var defaultProfileFiles embed.FS

// ProductionDefaults contains public routing settings and no node credentials.
func ProductionDefaults() AppState {
	data, err := defaultProfileFiles.ReadFile("default_profile.json")
	if err != nil {
		panic(err)
	}
	var state AppState
	if err := json.Unmarshal(data, &state); err != nil {
		panic(err)
	}
	if state.GeoData.Preset == "" {
		state.GeoData = DefaultGeoDataConfig()
	}
	state.UpdatedAt = time.Now().Format(time.RFC3339)
	return state
}
