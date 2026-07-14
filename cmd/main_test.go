package main

import (
	"testing"

	kubeheadroomv1alpha1 "github.com/karlkfi/kube-headroom/api/v1alpha1"
)

func TestRateLimitsFrom(t *testing.T) {
	cases := []struct {
		name      string
		hc        *kubeheadroomv1alpha1.HeadroomConfig
		wantQPS   float32
		wantBurst int
	}{
		{
			name:      "nil config uses defaults",
			hc:        nil,
			wantQPS:   defaultClientQPS,
			wantBurst: defaultClientBurst,
		},
		{
			name:      "unset fields use defaults",
			hc:        &kubeheadroomv1alpha1.HeadroomConfig{},
			wantQPS:   defaultClientQPS,
			wantBurst: defaultClientBurst,
		},
		{
			name: "explicit values win",
			hc: &kubeheadroomv1alpha1.HeadroomConfig{Spec: kubeheadroomv1alpha1.HeadroomConfigSpec{
				RateLimits: kubeheadroomv1alpha1.RateLimits{ClientQPS: 200, ClientBurst: 400},
			}},
			wantQPS:   200,
			wantBurst: 400,
		},
		{
			name: "one field set, the other defaults",
			hc: &kubeheadroomv1alpha1.HeadroomConfig{Spec: kubeheadroomv1alpha1.HeadroomConfigSpec{
				RateLimits: kubeheadroomv1alpha1.RateLimits{ClientQPS: 75},
			}},
			wantQPS:   75,
			wantBurst: defaultClientBurst,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			qps, burst := rateLimitsFrom(tc.hc)
			if qps != tc.wantQPS {
				t.Errorf("qps = %v, want %v", qps, tc.wantQPS)
			}
			if burst != tc.wantBurst {
				t.Errorf("burst = %v, want %v", burst, tc.wantBurst)
			}
		})
	}
}
