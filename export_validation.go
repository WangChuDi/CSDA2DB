package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var managedTables = func() map[string]bool {
	result := map[string]bool{}
	for _, name := range strings.Fields(`awp_hold_deaths bombs_defuse_start bombs_defused bombs_exploded bombs_plant_start bombs_planted chat_messages chicken_deaths chicken_positions clutches coaches damages data_utility decoys_start flashbangs_explode footsteps grenade_bounces grenade_positions grenade_projectiles_destroy he_grenades_explode hostage_pick_up_start hostage_picked_up hostage_positions hostage_rescued inferno_positions kills match player_buttons players players_buy players_economy players_flashed positions rounds shots smokes_start teams`) {
		result[name] = true
	}
	return result
}()

func validateHeaders(headers []string) error {
	seen := map[string]bool{"demo_id": true}
	for _, h := range headers {
		if h == "" || len(h) > 63 || strings.ContainsRune(h, 0) || seen[h] {
			return fmt.Errorf("invalid or duplicate CSV column %q", h)
		}
		seen[h] = true
	}
	if !seen["checksum"] && !seen["match_checksum"] {
		return fmt.Errorf("missing checksum column")
	}
	return nil
}

// Requiring the complete export set prevents an incomplete output directory
// from silently leaving stale tables behind during replacement.
func validateExportSet(files []string, demoBaseName string) (string, error) {
	seen := map[string]bool{}
	var checksum string
	for _, path := range files {
		name := filepath.Base(path)
		prefix := demoBaseName + "_"
		if !strings.HasPrefix(name, prefix) {
			return "", fmt.Errorf("unrelated CSV %s", name)
		}
		table := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".csv")
		if !managedTables[table] || seen[table] {
			return "", fmt.Errorf("unexpected table %s", table)
		}
		seen[table] = true
		if table != "match" {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		r := csv.NewReader(f)
		headers, err := r.Read()
		if err != nil {
			f.Close()
			return "", err
		}
		row, err := r.Read()
		if err != nil {
			f.Close()
			return "", err
		}
		for i, h := range headers {
			if normalizeColumnName(h) == "checksum" {
				checksum = row[i]
			}
		}
		_, err = r.Read()
		f.Close()
		if err != io.EOF {
			return "", fmt.Errorf("match CSV must have exactly one record")
		}
	}
	for table := range managedTables {
		if !seen[table] {
			return "", fmt.Errorf("incomplete CSDA export: missing %s", table)
		}
	}
	if checksum == "" {
		return "", fmt.Errorf("missing match checksum")
	}
	return checksum, nil
}
