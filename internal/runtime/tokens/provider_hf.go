//go:build tokenizers_hf

package tokens

import (
	"fmt"
	"strings"
	"sync"

	"github.com/daulet/tokenizers"
)

type hfProvider struct {
	mu        sync.Mutex
	instances map[string]*tokenizers.Tokenizer
}

func newExactProvider() exactProvider {
	return &hfProvider{
		instances: make(map[string]*tokenizers.Tokenizer),
	}
}

func (p *hfProvider) CountText(profile ModelProfile, cacheDir string, text string) (Estimate, error) {
	tk, err := p.load(profile, cacheDir)
	if err != nil {
		return Estimate{}, err
	}
	ids, _ := tk.Encode(text, false)
	return Estimate{
		Tokens:   len(ids),
		Exact:    true,
		Strategy: "hf_tokenizer_json",
	}, nil
}

func (p *hfProvider) load(profile ModelProfile, cacheDir string) (*tokenizers.Tokenizer, error) {
	if profile.HFTokenizerModelID == "" {
		return nil, fmt.Errorf("model %q has no configured tokenizer", profile.DisplayName)
	}
	key := profile.HFTokenizerModelID + "|" + strings.TrimSpace(cacheDir)

	p.mu.Lock()
	defer p.mu.Unlock()

	if tk, ok := p.instances[key]; ok {
		return tk, nil
	}

	opts := make([]tokenizers.TokenizerConfigOption, 0, 1)
	if strings.TrimSpace(cacheDir) != "" {
		opts = append(opts, tokenizers.WithCacheDir(cacheDir))
	}

	tk, err := tokenizers.FromPretrained(profile.HFTokenizerModelID, opts...)
	if err != nil {
		return nil, err
	}
	p.instances[key] = tk
	return tk, nil
}
