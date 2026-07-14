package benchmark

import "context"

func Run(ctx context.Context, root string, manifest Manifest) (report Report, retErr error) {
	normalizeSuiteManifest(&manifest, root)
	execution, err := acquireBenchmarkExecution(ctx, root, manifest.ID)
	if err != nil {
		return Report{}, err
	}
	defer func() {
		if err := execution.Close(); retErr == nil && err != nil {
			retErr = err
		}
	}()
	taskExecutions, err := acquireSuiteTaskExecutions(execution.Context, root, manifest.Items)
	if err != nil {
		return Report{}, err
	}
	defer func() {
		if err := closeSuiteTaskExecutions(taskExecutions); retErr == nil && err != nil {
			retErr = err
		}
	}()
	return runReport(execution.Context, root, manifest)
}

func runReport(ctx context.Context, root string, manifest Manifest) (Report, error) {
	results := make([]ItemResult, 0, len(manifest.Items))
	for _, item := range manifest.Items {
		result, err := ScoreItem(ctx, root, item)
		if err != nil {
			return Report{}, err
		}
		results = append(results, result)
	}
	if err := checkBenchmarkExecution(ctx); err != nil {
		return Report{}, err
	}
	return WriteReport(root, manifest, results)
}

func Verify(ctx context.Context, root string, manifest Manifest, opts AcceptanceOptions) (report Report, retErr error) {
	normalizeSuiteManifest(&manifest, root)
	execution, err := acquireBenchmarkExecution(ctx, root, manifest.ID)
	if err != nil {
		return Report{}, err
	}
	defer func() {
		if err := execution.Close(); retErr == nil && err != nil {
			retErr = err
		}
	}()
	ctx = execution.Context
	taskExecutions, err := acquireSuiteTaskExecutions(ctx, root, manifest.Items)
	if err != nil {
		return Report{}, err
	}
	defer func() {
		if err := closeSuiteTaskExecutions(taskExecutions); retErr == nil && err != nil {
			retErr = err
		}
	}()
	for i := range manifest.Items {
		item := &manifest.Items[i]
		if hasAcceptanceChecks(*item) && taskHasNonEmptyPatch(root, item.TaskID) {
			itemCtx := taskExecutions[item.TaskID].Context
			if err := checkBenchmarkExecution(itemCtx); err != nil {
				return Report{}, err
			}
			if _, err := RunAcceptanceChecks(itemCtx, root, *item, opts); err != nil {
				return Report{}, err
			}
		}
	}
	return runReport(ctx, root, manifest)
}
