package repository

import (
	"fmt"
	"strings"
)

// GlobalRankBucket identifies one of the three rotating rank generations.
// The numeric value is part of the persisted ordering contract: SQLite BINARY
// and PostgreSQL C collation sort bucket 0 before 1 before 2.
type GlobalRankBucket uint8

const (
	GlobalRankBucket0 GlobalRankBucket = iota
	GlobalRankBucket1
	GlobalRankBucket2
)

// GlobalRank is the parsed representation of an items.frac_index value after
// the 0.8.5 checkpoint. Fraction is a legacy fractional-index payload; the
// bucket is deliberately kept separate so migration code cannot accidentally
// treat the generation prefix as part of the fractional-index algorithm.
type GlobalRank struct {
	Bucket   GlobalRankBucket
	Fraction string
}

// EncodeGlobalRank returns the canonical persisted form bucket|fraction.
func EncodeGlobalRank(bucket GlobalRankBucket, fraction string) (string, error) {
	if err := validateGlobalRankBucket(bucket); err != nil {
		return "", err
	}
	if err := validateGlobalRankFraction(fraction); err != nil {
		return "", fmt.Errorf("invalid global rank fraction: %w", err)
	}
	if strings.ContainsRune(fraction, '|') {
		return "", fmt.Errorf("invalid global rank fraction: contains bucket separator")
	}
	return fmt.Sprintf("%d|%s", bucket, fraction), nil
}

// ParseGlobalRank validates and splits a persisted bucket-prefixed rank.
// Validation is intentionally strict: accepting malformed values here would
// make the bytewise ordering contract impossible to reason about during a
// resumable migration.
func ParseGlobalRank(value string) (GlobalRank, error) {
	if len(value) < 3 || value[1] != '|' {
		return GlobalRank{}, fmt.Errorf("invalid global rank %q: want bucket|fraction", value)
	}

	var bucket GlobalRankBucket
	switch value[0] {
	case '0':
		bucket = GlobalRankBucket0
	case '1':
		bucket = GlobalRankBucket1
	case '2':
		bucket = GlobalRankBucket2
	default:
		return GlobalRank{}, fmt.Errorf("invalid global rank bucket %q", value[:1])
	}

	fraction := value[2:]
	if err := validateGlobalRankFraction(fraction); err != nil {
		return GlobalRank{}, fmt.Errorf("invalid global rank fraction: %w", err)
	}
	return GlobalRank{Bucket: bucket, Fraction: fraction}, nil
}

// WithGlobalRankBucket changes only the generation prefix while preserving
// the fractional payload and therefore the item's position within a migrated
// bucket. It is used by the online rebalance worker.
func WithGlobalRankBucket(value string, bucket GlobalRankBucket) (string, error) {
	rank, err := ParseGlobalRank(value)
	if err != nil {
		return "", err
	}
	return EncodeGlobalRank(bucket, rank.Fraction)
}

// GlobalRankBucketTransition returns the next empty bucket and migration
// direction for a full normalization cycle. 0→1 and 1→2 migrate the high end
// downward because the new bucket sorts after the old bucket. 2→0 migrates
// the low end upward because bucket 0 sorts before bucket 2.
func GlobalRankBucketTransition(active GlobalRankBucket) (target GlobalRankBucket, direction string, err error) {
	if err := validateGlobalRankBucket(active); err != nil {
		return 0, "", err
	}
	switch active {
	case GlobalRankBucket0:
		return GlobalRankBucket1, "high_to_low", nil
	case GlobalRankBucket1:
		return GlobalRankBucket2, "high_to_low", nil
	case GlobalRankBucket2:
		return GlobalRankBucket0, "low_to_high", nil
	default:
		return 0, "", fmt.Errorf("invalid active global rank bucket %d", active)
	}
}

func validateGlobalRankBucket(bucket GlobalRankBucket) error {
	if bucket > GlobalRankBucket2 {
		return fmt.Errorf("invalid global rank bucket %d", bucket)
	}
	return nil
}

func validateGlobalRankFraction(fraction string) error {
	// zero is the unique first key emitted by KeyBetween("", ""). It is the
	// one valid fractional key whose trailing zero is intentionally accepted.
	if fraction == zero {
		return nil
	}
	if err := validateOrderKey(fraction); err != nil {
		return err
	}
	for i := 0; i < len(fraction); i++ {
		if strings.IndexByte(base62Digits, fraction[i]) < 0 {
			return fmt.Errorf("invalid global rank fraction: character %q is not base62", fraction[i])
		}
	}
	return nil
}
