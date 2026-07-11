package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	domain "github.com/emfont/emfont/backend/internal/domain/font"
)

func TestNormalizeFontRepositoryConfigDefaultsTerminalFailureBound(t *testing.T) {
	cfg, err := normalizeFontRepositoryConfig(nil)
	if err != nil {
		t.Fatalf("normalizeFontRepositoryConfig: %v", err)
	}
	if defaultMaxTerminalFailures != 10_000 {
		t.Fatalf("defaultMaxTerminalFailures = %d, want 10000", defaultMaxTerminalFailures)
	}
	if cfg.MaxTerminalFailures != defaultMaxTerminalFailures {
		t.Fatalf("MaxTerminalFailures = %d, want 10000", cfg.MaxTerminalFailures)
	}
}

func TestNormalizeFontRepositoryConfigRejectsInvalidTerminalFailureBound(t *testing.T) {
	_, err := normalizeFontRepositoryConfig([]FontRepositoryConfig{
		{MaxArtifacts: 1, MaxAccountedBytes: 1, ArtifactReservation: 1, MaxTerminalFailures: -1},
	})
	if err == nil {
		t.Fatal("normalizeFontRepositoryConfig error = nil")
	}
}

func TestQuotaMutationsRejectUnboundQuerier(t *testing.T) {
	repository := NewFontRepository(NewQueries(nil), FontRepositoryConfig{
		MaxArtifacts: 10, MaxAccountedBytes: 10, ArtifactReservation: 1, MaxTerminalFailures: 10,
	})
	artifact := domain.Artifact{Key: "unbound"}
	claim := domain.BuildClaim{Owner: "owner", Fence: 1}

	tests := []struct {
		name string
		run  func() error
	}{
		{name: "create", run: func() error { return repository.CreateFontArtifact(context.Background(), artifact) }},
		{name: "mark missing", run: func() error {
			_, err := repository.MarkFontArtifactMissing(context.Background(), artifact.Key, "object", 1, "missing")
			return err
		}},
		{name: "acquire", run: func() error {
			_, _, err := repository.AcquireBuildJob(context.Background(), artifact.Key, claim.Owner, time.Minute)
			return err
		}},
		{name: "terminal failure", run: func() error {
			return repository.FailBuildJobTerminal(
				context.Background(), artifact.Key, claim, domain.FailureCodeUnsupportedCodepoints, "unsupported",
			)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); !errors.Is(err, ErrArtifactQuotaTransactionRequired) {
				t.Fatalf("quota mutation error = %v, want ErrArtifactQuotaTransactionRequired", err)
			}
		})
	}
}

func TestNewFontRepositoryFromTxRejectsNilTransaction(t *testing.T) {
	repository := NewFontRepositoryFromTx(nil)
	err := repository.CreateFontArtifact(context.Background(), domain.Artifact{Key: "nil-transaction"})
	if !errors.Is(err, ErrArtifactQuotaTransactionRequired) {
		t.Fatalf("nil transaction error = %v, want ErrArtifactQuotaTransactionRequired", err)
	}
}
