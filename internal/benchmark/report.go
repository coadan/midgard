package benchmark

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"midgard/internal/state"
	"midgard/internal/workbench"
)

type Report struct {
	ManifestID            string
	Results               []ItemResult
	Path                  string
	ReferenceEvidencePath string
	RunID                 string
	RunStatus             string
}

func WriteReport(root string, manifest Manifest, results []ItemResult) (Report, error) {
	return writeReport(root, manifest, results, nil)
}

func WriteReportForRun(root string, manifest Manifest, results []ItemResult, run state.BenchmarkRun) (Report, error) {
	return writeReport(root, manifest, results, &run)
}

func writeReport(root string, manifest Manifest, results []ItemResult, run *state.BenchmarkRun) (Report, error) {
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
	if run != nil {
		fmt.Fprintf(&b, "- benchmark_run_id: %s\n", run.ID)
		fmt.Fprintf(&b, "- benchmark_run_status: %s\n", run.Status)
	}
	b.WriteString("- hidden_reference_evidence: ")
	b.WriteString(filepath.Base(referencePath))
	b.WriteString("\n")
	b.WriteString("- worker_context_excludes_hidden_references: true\n\n")
	writeReportSummary(&b, results)
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
		if result.Evidence.RunError != "" {
			fmt.Fprintf(&b, "- run_error: %s\n", singleLine(result.Evidence.RunError))
			if result.Evidence.RunErrorClass != "" {
				fmt.Fprintf(&b, "- run_error_class: %s\n", result.Evidence.RunErrorClass)
			}
		}
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
		if result.Evidence.AcceptanceRequired {
			fmt.Fprintf(
				&b,
				"- acceptance: status:%s valid:%t passed:%t\n",
				result.Evidence.AcceptanceStatus,
				result.Evidence.AcceptanceValid,
				result.Evidence.AcceptancePassed,
			)
			writeEvidence(&b, "acceptance_summary", result.Evidence.AcceptancePath)
			if result.Evidence.AcceptanceChecksum != "" {
				fmt.Fprintf(&b, "- acceptance_checksum: %s\n", result.Evidence.AcceptanceChecksum)
			}
			if result.Evidence.AcceptanceReason != "" {
				fmt.Fprintf(&b, "- acceptance_reason: %s\n", result.Evidence.AcceptanceReason)
			}
			for _, check := range result.Evidence.AcceptanceChecks {
				fmt.Fprintf(
					&b,
					"- acceptance_check: id:%s repo:%s status:%s expected_exit:%d exit:%d timeout:%t stdout_truncated:%t stderr_truncated:%t result:artifact:%s stdout:artifact:%s stderr:artifact:%s\n",
					check.ID, check.RepoID, check.Status, check.ExpectedExitCode, check.ExitCode, check.TimedOut,
					check.StdoutTruncated, check.StderrTruncated,
					check.ResultPath, check.StdoutPath, check.StderrPath,
				)
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
	if err := writeBenchmarkFileAtomically(reportPath, []byte(b.String())); err != nil {
		return Report{}, err
	}
	report := Report{ManifestID: manifest.ID, Results: results, Path: reportPath, ReferenceEvidencePath: referencePath}
	if run != nil {
		report.RunID = run.ID
		report.RunStatus = run.Status
	}
	return report, nil
}

func writeReportSummary(b *strings.Builder, results []ItemResult) {
	counts := map[Score]int{}
	var totalCost float64
	var acceptanceRequired, acceptancePassed, runErrors int
	for _, result := range results {
		counts[result.Score]++
		totalCost += result.Evidence.CostUSD
		if result.Evidence.AcceptanceRequired {
			acceptanceRequired++
		}
		if result.Evidence.AcceptancePassed {
			acceptancePassed++
		}
		if result.Evidence.RunError != "" {
			runErrors++
		}
	}
	passRate := 0.0
	if len(results) > 0 {
		passRate = float64(counts[ScorePass]) / float64(len(results))
	}
	b.WriteString("## Summary\n\n")
	fmt.Fprintf(b, "- items: %d\n", len(results))
	fmt.Fprintf(b, "- scores: pass:%d partial:%d fail:%d invalid:%d\n", counts[ScorePass], counts[ScorePartial], counts[ScoreFail], counts[ScoreInvalid])
	fmt.Fprintf(b, "- pass_rate: %.4f\n", passRate)
	fmt.Fprintf(b, "- acceptance: required:%d passed:%d\n", acceptanceRequired, acceptancePassed)
	fmt.Fprintf(b, "- run_errors: %d\n", runErrors)
	fmt.Fprintf(b, "- total_cost_usd: %.6f\n\n", totalCost)
}

func singleLine(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.TrimSpace(value)
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
	return writeBenchmarkFileAtomically(path, append(data, '\n'))
}

func writeBenchmarkFileAtomically(path string, data []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".midgard-benchmark-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if err = tmp.Chmod(0o644); err != nil {
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
