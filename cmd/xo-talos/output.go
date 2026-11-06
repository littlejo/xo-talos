package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// result describes the template of one pool.
type result struct {
	TemplateUUID string `json:"template_uuid"`
	TemplateName string `json:"template_name"`
	PoolUUID     string `json:"pool_uuid"`
	SRUUID       string `json:"sr_uuid"`
	TalosVersion string `json:"talos_version"`
	SchematicID  string `json:"schematic_id"`
	Created      bool   `json:"created"`  // false: it already existed
	Verified     bool   `json:"verified"` // --verify passed in this run
	// Pruned lists the templates --prune-keep deleted (or, with
	// --prune-dry-run, would delete).
	Pruned []string `json:"pruned,omitempty"`
}

var outputFormats = []string{"text", "json", "env"}

// writeResults prints the results on stdout:
//   - text: one template UUID per line;
//   - json: an array with one object per pool;
//   - env: shell exports of the clusterctl variables CAPV's Talos templates
//     use, for eval "$(xo-talos --output env ...)". It needs a single
//     pool.
func writeResults(w io.Writer, format string, results []result) error {
	switch format {
	case "text":
		for _, r := range results {
			if _, err := fmt.Fprintln(w, r.TemplateUUID); err != nil {
				return err
			}
		}
		return nil
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	case "env":
		if len(results) != 1 {
			return errors.New("--output env needs exactly one template; use --output json for several pools")
		}
		r := results[0]
		_, err := fmt.Fprintf(w, "export XO_TEMPLATE_UUID='%s'\nexport XO_POOL_UUID='%s'\nexport TALOS_VERSION='%s'\n",
			r.TemplateUUID, r.PoolUUID, r.TalosVersion)
		return err
	default:
		return fmt.Errorf("unknown output format %q", format)
	}
}
