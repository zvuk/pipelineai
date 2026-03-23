//go:build !tokenizers_hf

package tokens

import "fmt"

type stubProvider struct{}

func newExactProvider() exactProvider {
	return stubProvider{}
}

func (stubProvider) CountText(_ ModelProfile, _ string, _ string) (Estimate, error) {
	return Estimate{}, fmt.Errorf("hf tokenizer backend is not enabled in this build")
}
