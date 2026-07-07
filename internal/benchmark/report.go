package benchmark

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"midgard/internal/workbench"
)

type Report struct {
	ManifestID            string
	Results               []ItemResult
	Path                  string
	ReferenceEvidencePath string
}

func WriteReport(root string, manifest Manifest, results []ItemResult) (Report, error) {
	status, err := workbench.Status(root)
	if err != nil {
		return Report{}, err
	}
	benchmarkDir := filepath.Join(status.Root, ".midgard", "benchmarks")
	reportPath := filepath.Join(benchmarkDir, manifest.ID+".mdx")
	referencePath := filepath.Join(benchmarkDir, manifest.ID+"-reference-evidence.json")
	if err := os.MkdirAll(benchmarkDir, 0o755); err != nil {
		return Report{}, err
	}
	if err := writeReferenceEvidence(referencePath, manifest); err != nil {
		return Report{}, err
	}
	var b strings.Builder
	b.WriteString("# Benchmark ")
	b.WriteString(manifest.ID)
	b.WriteString("\n\n")
	b.WriteString("- hidden_reference_evidence: ")
	b.WriteString(filepath.Base(referencePath))
	b.WriteString("\n")
	b.WriteString("- worker_context_excludes_hidden_references: true\n\n")
	for _, result := range results {
		b.WriteString("## ")
		b.WriteString(result.ItemID)
		b.WriteString("\n\n")
		b.WriteString("- score: ")
		b.WriteString(string(result.Score))
		b.WriteString("\n")
		b.WriteString("- task: ")
		b.WriteString(result.Evidence.TaskID)
		b.WriteString("\n")
		b.WriteString("- state: ")
		b.WriteString(result.Evidence.TaskState)
		b.WriteString("\n")
		writeEvidence(&b, "plan", result.Evidence.PlanPath)
		writeEvidence(&b, "implementation", result.Evidence.ReportPath)
		writeEvidence(&b, "review", result.Evidence.ReviewPath)
		writeEvidence(&b, "patch", result.Evidence.PatchPath)
		if result.Evidence.PatchPath != "" {
			fmt.Fprintf(&b, "- patch_bytes: %d\n", result.Evidence.PatchBytes)
		}
		fmt.Fprintf(&b, "- cost_usd: %.6f\n", result.Evidence.CostUSD)
		for _, caveat := range result.Evidence.CostCaveats {
			fmt.Fprintf(&b, "- cost_caveat: %s\n", caveat)
		}
		if result.Evidence.ReferencePatchChecked {
			fmt.Fprintf(&b, "- reference_patch_match: %t\n", result.Evidence.ReferencePatchMatched)
		}
		if result.Evidence.ExpectedFilesChecked {
			fmt.Fprintf(&b, "- expected_touched_files_match: %t\n", result.Evidence.ExpectedFilesMatched)
			if len(result.Evidence.ExpectedTouchedFiles) > 0 {
				fmt.Fprintf(&b, "- expected_touched_files: %s\n", strings.Join(result.Evidence.ExpectedTouchedFiles, ","))
			}
			if len(result.Evidence.TouchedFiles) > 0 {
				fmt.Fprintf(&b, "- touched_files: %s\n", strings.Join(result.Evidence.TouchedFiles, ","))
			}
		}
		for _, providerModel := range result.Evidence.ProviderModels {
			fmt.Fprintf(
				&b,
				"- provider_model: role:%s provider:%s model:%s provider_fingerprint:%s usage:in=%d,out=%d\n",
				providerModel.Role,
				providerModel.ProviderID,
				providerModel.ModelID,
				providerModel.ProviderFingerprint,
				providerModel.InputTokens,
				providerModel.OutputTokens,
			)
		}
		b.WriteString("\n")
	}
	if err := os.WriteFile(reportPath, []byte(b.String()), 0o644); err != nil {
		return Report{}, err
	}
	return Report{ManifestID: manifest.ID, Results: results, Path: reportPath, ReferenceEvidencePath: referencePath}, nil
}

func writeEvidence(b *strings.Builder, label, path string) {
	if path == "" {
		return
	}
	fmt.Fprintf(b, "- %s: artifact:%s\n", label, path)
}

func writeReferenceEvidence(path string, manifest Manifest) error {
	type referenceItem struct {
		ItemID               string        `json:"item_id"`
		HiddenReferencePRs   []ReferencePR `json:"hidden_reference_prs"`
		HiddenReferencePatch string        `json:"hidden_reference_patch,omitempty"`
	}
	items := make([]referenceItem, 0, len(manifest.Items))
	for _, item := range manifest.Items {
		items = append(items, referenceItem{
			ItemID:               item.ID,
			HiddenReferencePRs:   append([]ReferencePR(nil), item.HiddenReferencePRs...),
			HiddenReferencePatch: item.HiddenReferencePatch,
		})
	}
	data, err := json.MarshalIndent(map[string]any{
		"manifest_id": manifest.ID,
		"items":       items,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
