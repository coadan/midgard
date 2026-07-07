package benchmark

import "context"

func Run(ctx context.Context, root string, manifest Manifest) (Report, error) {
	results := make([]ItemResult, 0, len(manifest.Items))
	for _, item := range manifest.Items {
		result, err := ScoreItem(ctx, root, item)
		if err != nil {
			return Report{}, err
		}
		results = append(results, result)
	}
	return WriteReport(root, manifest, results)
}
