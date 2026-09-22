package telemetry_test

import (
	"context"
	"testing"

	"github.com/xrodriguezb/fulcrum/internal/platform/config"
	"github.com/xrodriguezb/fulcrum/internal/platform/telemetry"
)

// Setup builds a resource by merging with the SDK default, and that merge fails
// when the two carry different schema urls. It failed exactly that way at
// startup and nothing caught it, because no test had ever called Setup.
func TestSetupBuildsAProviderForEveryExporter(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		exporter string
		enabled  bool
	}{
		{name: "stdout", exporter: "stdout", enabled: true},
		{name: "disabled", exporter: "none", enabled: false},
		{name: "tracing switched off", exporter: "stdout", enabled: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := config.Config{
				Env:         config.EnvDevelopment,
				ServiceName: "fulcrum-test",
				Telemetry: config.TelemetryConfig{
					TracingEnabled: tc.enabled,
					Exporter:       tc.exporter,
					SampleRatio:    1,
				},
			}

			shutdown, err := telemetry.Setup(t.Context(), cfg)
			if err != nil {
				t.Fatalf("Setup returned %v", err)
			}
			if shutdown == nil {
				t.Fatalf("Setup returned no shutdown function")
			}
			if err := shutdown(context.Background()); err != nil {
				t.Errorf("shutdown returned %v", err)
			}
		})
	}
}

func TestTraceIDIsEmptyWithoutASpan(t *testing.T) {
	t.Parallel()

	if got := telemetry.TraceIDFrom(t.Context()); got != "" {
		t.Errorf("TraceIDFrom = %q, want empty outside a span", got)
	}
}
