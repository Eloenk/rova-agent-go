package nanopay

import (
	"math/rand"
	"sync"
	"time"
)

type QuoteResult struct {
	Provider string  `json:"provider"`
	Rate     float64 `json:"rate"`
	PaidUsdc float64 `json:"paidUsdc"`
}

type QuoteShopResult struct {
	ProvidersChecked int           `json:"providersChecked"`
	BestProvider     string        `json:"bestProvider"`
	BestRate         float64       `json:"bestRate"`
	Quotes           []QuoteResult `json:"quotes"`
}

type Shopper struct{}

func NewShopper() *Shopper {
	return &Shopper{}
}

func (s *Shopper) ShopRates(pair string) *QuoteShopResult {
	providers := []string{"provider-a", "provider-b", "provider-c"}
	quoteChan := make(chan QuoteResult, len(providers))
	var wg sync.WaitGroup

	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	for _, p := range providers {
		wg.Add(1)
		go func(provider string) {
			defer wg.Done()

			baseRate := 0.940
			drift := (r.Float64() - 0.5) * 0.010
			rate := baseRate + drift

			if provider == "provider-b" {
				rate += 0.002
			}

			quoteChan <- QuoteResult{
				Provider: provider,
				Rate:     rate,
				PaidUsdc: 0.0001,
			}
		}(p)
	}

	wg.Wait()
	close(quoteChan)

	quotes := make([]QuoteResult, 0, len(providers))
	var bestProvider string
	bestRate := 0.0

	for q := range quoteChan {
		quotes = append(quotes, q)
		if q.Rate > bestRate {
			bestRate = q.Rate
			bestProvider = q.Provider
		}
	}

	return &QuoteShopResult{
		ProvidersChecked: len(quotes),
		BestProvider:     bestProvider,
		BestRate:         bestRate,
		Quotes:           quotes,
	}
}
