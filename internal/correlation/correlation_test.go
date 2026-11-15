package correlation_test

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"strings"
	"testing"

	"github.com/code-corhuila/multi-tour-workflow-register-payment/internal/correlation"
)

func TestC_AContextCarriesItsCorrelationId(t *testing.T) {
	if got := correlation.From(context.Background()); got != "" {
		t.Errorf("an empty context gave %q", got)
	}
	if got := correlation.From(correlation.With(context.Background(), "abc-1")); got != "abc-1" {
		t.Errorf("got %q, want abc-1", got)
	}
}

func TestC_ANewIdIsAUuidAndNeverRepeats(t *testing.T) {
	uuid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	first, second := correlation.NewID(), correlation.NewID()
	if !uuid.MatchString(first) || first == second {
		t.Errorf("ids %q and %q are not distinct version 4 UUIDs", first, second)
	}
}

// AC-WF2-08: a line logged with the context of a request carries its correlation id; one without it carries none.
func TestLogHandler_adds_the_correlation_id_of_the_context(t *testing.T) {
	var out bytes.Buffer
	log := slog.New(correlation.NewLogHandler(slog.NewJSONHandler(&out, nil)))

	log.InfoContext(correlation.With(context.Background(), "corr-0123456789"), "with", "sagaId", "s-1")
	log.InfoContext(context.Background(), "without")

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"correlationId":"corr-0123456789"`) || !strings.Contains(lines[0], `"sagaId":"s-1"`) ||
		strings.Contains(lines[1], "correlationId") {
		t.Errorf("lines %q", lines)
	}
}
