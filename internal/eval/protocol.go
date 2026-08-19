package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"midgard/internal/protocol"
)

type Manifest struct {
	Cases []ProtocolCase `json:"cases"`
}

type ProtocolCase struct {
	Name               string          `json:"name"`
	Input              string          `json:"input"`
	ExpectedActions    int             `json:"expected_actions"`
	ExpectedName       string          `json:"expected_name,omitempty"`
	ExpectedArguments  json.RawMessage `json:"expected_arguments,omitempty"`
	ExpectedRejections int             `json:"expected_rejections,omitempty"`
}

type CaseResult struct {
	Name       string `json:"name"`
	Passed     bool   `json:"passed"`
	Events     int    `json:"events"`
	Actions    int    `json:"actions"`
	Rejections int    `json:"rejections"`
}

type Report struct {
	Protocol           string       `json:"protocol"`
	Profile            string       `json:"profile"`
	ProfileVersion     string       `json:"profile_version"`
	ProfileFingerprint string       `json:"profile_fingerprint"`
	Cases              []CaseResult `json:"cases"`
	Passed             int          `json:"passed"`
	Decision           string       `json:"decision"`
}

func LoadManifest(path string) (Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Manifest{}, err
	}
	base := filepath.Dir(path)
	for index := range manifest.Cases {
		if !filepath.IsAbs(manifest.Cases[index].Input) {
			manifest.Cases[index].Input = filepath.Join(base, manifest.Cases[index].Input)
		}
	}
	return manifest, nil
}

func Score(manifest Manifest) (Report, error) {
	probe, err := protocol.NewTurn()
	if err != nil {
		return Report{}, err
	}
	negotiation := probe.Negotiation()
	report := Report{Protocol: negotiation.Protocol, Profile: negotiation.Profile,
		ProfileVersion: negotiation.ProfileVersion, ProfileFingerprint: negotiation.ProfileFingerprint,
		Decision: "Bragi is Midgard's sole model protocol; fixtures verify decode, repair, rejection, commit, and effect extraction"}
	for _, test := range manifest.Cases {
		raw, err := os.ReadFile(test.Input)
		if err != nil {
			return Report{}, fmt.Errorf("case %s: %w", test.Name, err)
		}
		turn, err := protocol.NewTurn()
		if err != nil {
			return Report{}, err
		}
		turn.Write(string(raw))
		turn.FinishCompleted()
		actions, actionErr := turn.HostActions()
		rejections := 0
		for _, event := range turn.Events() {
			if event.Kind == "source.rejected" || event.Kind == "op.rejected" || event.Kind == "commit.rejected" {
				rejections++
			}
		}
		passed := actionErr == nil && len(actions) == test.ExpectedActions && rejections == test.ExpectedRejections
		if passed && test.ExpectedName != "" {
			passed = len(actions) > 0 && actions[len(actions)-1].Name == test.ExpectedName
		}
		if passed && len(test.ExpectedArguments) > 0 {
			passed = len(actions) > 0 && jsonEqual(actions[len(actions)-1].Arguments, test.ExpectedArguments)
		}
		result := CaseResult{Name: test.Name, Passed: passed, Events: len(turn.Events()), Actions: len(actions), Rejections: rejections}
		report.Cases = append(report.Cases, result)
		if passed {
			report.Passed++
		}
	}
	return report, nil
}

func jsonEqual(left, right []byte) bool {
	var a, b any
	return json.Unmarshal(left, &a) == nil && json.Unmarshal(right, &b) == nil && fmt.Sprintf("%#v", a) == fmt.Sprintf("%#v", b)
}
