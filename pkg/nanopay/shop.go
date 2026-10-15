package nanopay

import (
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"time"
)

type QuoteResult struct {
	Provider string  `json:"provider"`
	Rate     float64 `json:"rate"`
	PaidUsdc float64 `json:"paidUsdc"`
	TxRef    string  `json:"txRef"`
}

type QuoteShopResult struct {
	ProvidersChecked int           `json:"providersChecked"`
	BestProvider     string        `json:"bestProvider"`
	BestRate         float64       `json:"bestRate"`
	Executable       bool          `json:"executable"`
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

	for _, p := range providers {
		wg.Add(1)
		go func(provider string) {
			defer wg.Done()

			seed := time.Now().UnixNano() + int64(len(pair)*1000) + int64(provider[len(provider)-1])
			r := rand.New(rand.NewSource(seed))

			baseRate := 0.940
			drift := (r.Float64() - 0.5) * 0.010
			rate := baseRate + drift

			if provider == "provider-b" {
				rate += 0.002
			}

			quoteChan <- QuoteResult{
				Provider: provider,
				Rate:     rate,
				PaidUsdc: 0,
				TxRef:    fmt.Sprintf("quote-simulation-go-%s-%d", provider, time.Now().UnixNano()),
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
	sort.Slice(quotes, func(i, j int) bool {
		return quotes[i].Provider < quotes[j].Provider
	})

	return &QuoteShopResult{
		ProvidersChecked: len(quotes),
		BestProvider:     bestProvider,
		BestRate:         bestRate,
		Executable:       false,
		Quotes:           quotes,
	}
}
