package tokens

type exactProvider interface {
	CountText(profile ModelProfile, cacheDir string, text string) (Estimate, error)
}
