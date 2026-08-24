// Package catalog maintains the farm/nectar inspection-rule catalog: fictional
// farms, nectar flowering periods, extraction-room batch summaries, temporary
// storage zones, personnel qualifications, pollen classification sets, and the
// DNA and chemistry thresholds. It also provides rule-version freshness checks.
//
// Component: 蜂场蜜源与检验规则目录.
package catalog

import (
	"errors"
	"sync"
	"time"

	"nectargate/raw-honey-maturation-intake/fixed"
)

// FarmID identifies a honey farm.
type FarmID string

// NectarSeason identifies a honey-source flowering period (蜜源花期).
type NectarSeason string

// PersonnelID identifies a qualified staff member.
type PersonnelID string

// TemporaryZone identifies a temporary storage temperature zone (暂存温区).
type TemporaryZone string

// PollenClass is a locked pollen classification.
type PollenClass string

// Locked pollen classifications required by the microscope coverage rule.
const (
	PollenTarget       PollenClass = "target"       // 目标花粉
	PollenAccompanying PollenClass = "accompanying" // 伴生花粉
	PollenUnknown      PollenClass = "unknown"      // 未知颗粒
	PollenContaminant  PollenClass = "contaminant"  // 污染颗粒
)

// AllPollenClasses is the canonical locked classification set.
var AllPollenClasses = []PollenClass{
	PollenTarget, PollenAccompanying, PollenUnknown, PollenContaminant,
}

// BatchSummary is the honey-extraction room batch summary (摇蜜房批次摘要).
type BatchSummary struct {
	BatchID     string
	ExtractedAt time.Time
	// ValidFor is how long the summary is considered fresh.
	ValidFor time.Duration
}

// DNAThreshold holds the locked DNA fingerprint threshold for a rule version.
type DNAThreshold struct {
	// MaxCt is the maximum acceptable qPCR Ct value at scale 2.
	MaxCt fixed.Decimal
}

// PollenThreshold holds the locked pollen-spectrum threshold (花粉谱阈值). The
// target-pollen count must meet the minimum to accept a spectrum.
type PollenThreshold struct {
	MinTargetCount int
}

// ChemistryThreshold holds the locked chemistry thresholds for a rule version.
type ChemistryThreshold struct {
	MaxHMF          fixed.Decimal // HMF upper bound
	MinAmylase      fixed.Decimal // amylase activity lower bound
	MaxMoisture     fixed.Decimal // moisture upper bound
	MaxConductivity fixed.Decimal // conductivity upper bound
	MaxAcidity      fixed.Decimal // acidity upper bound
}

// FarmSourceRule is the frozen catalog entry used to validate a lock request.
type FarmSourceRule struct {
	Farm          FarmID
	NectarSeason  NectarSeason
	Summary       BatchSummary
	ZoneRange     []TemporaryZone
	Qualified     map[PersonnelID]bool
	PollenClasses []PollenClass
	DNA           DNAThreshold
	Pollen        PollenThreshold
	Chemistry     ChemistryThreshold
	Version       int64
}

// ValidationError reports a concrete catalog validation failure.
type ValidationError struct{ Reason string }

func (e *ValidationError) Error() string { return "catalog: " + e.Reason }

// Sentinel catalog validation errors.
var (
	ErrFarmMismatch   = &ValidationError{Reason: "farm mismatch"}
	ErrSeasonMismatch = &ValidationError{Reason: "nectar season mismatch"}
	ErrStaleSummary   = &ValidationError{Reason: "stale batch summary"}
	ErrZoneOutOfRange = &ValidationError{Reason: "temporary zone out of range"}
	ErrUnqualified    = &ValidationError{Reason: "unqualified personnel"}
	ErrStaleRule      = &ValidationError{Reason: "stale rule version"}
)

// ErrUnknownFarm is returned when a farm is not present in the catalog.
var ErrUnknownFarm = errors.New("catalog: unknown farm")

// RuleCatalog is the read side of the rule catalog. It validates that a lock
// request is consistent with a farm's rules and that the referenced rule
// version is still fresh.
type RuleCatalog interface {
	// Rule returns the rule for a farm, or nil when unknown.
	Rule(farm FarmID) *FarmSourceRule
	// IsFresh reports whether the given rule version is still current.
	IsFresh(version int64) bool
}

// ValidateLock checks season, batch-summary freshness and zone range against
// the rule. now is the logical "current" time used for summary freshness.
func (r *FarmSourceRule) ValidateLock(season NectarSeason, zone TemporaryZone, now time.Time) error {
	if r.NectarSeason != season {
		return ErrSeasonMismatch
	}
	if now.Sub(r.Summary.ExtractedAt) > r.Summary.ValidFor {
		return ErrStaleSummary
	}
	if !r.containsZone(zone) {
		return ErrZoneOutOfRange
	}
	return nil
}

// ValidatePersonnel reports ErrUnqualified when any listed person is missing
// from the qualified set.
func (r *FarmSourceRule) ValidatePersonnel(ids ...PersonnelID) error {
	for _, id := range ids {
		if !r.Qualified[id] {
			return ErrUnqualified
		}
	}
	return nil
}

func (r *FarmSourceRule) containsZone(z TemporaryZone) bool {
	for _, zone := range r.ZoneRange {
		if zone == z {
			return true
		}
	}
	return false
}

// IsPollenClass reports whether c is a locked pollen classification.
func (r *FarmSourceRule) IsPollenClass(c PollenClass) bool {
	for _, p := range r.PollenClasses {
		if p == c {
			return true
		}
	}
	return false
}

// Catalog is a concurrency-safe mutable rule catalog used by the running
// service. It is seeded from reference data at startup and implements
// RuleCatalog so the API can depend on the read-only contract.
type Catalog struct {
	mu    sync.RWMutex
	rules map[FarmID]*FarmSourceRule
}

// NewCatalog returns an empty catalog.
func NewCatalog() *Catalog {
	return &Catalog{rules: make(map[FarmID]*FarmSourceRule)}
}

// Add registers a rule for a farm, replacing any previous rule.
func (c *Catalog) Add(r *FarmSourceRule) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rules[r.Farm] = r
}

// Rule implements RuleCatalog.
func (c *Catalog) Rule(farm FarmID) *FarmSourceRule {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rules[farm]
}

// IsFresh reports whether a rule version is the current version of some farm.
func (c *Catalog) IsFresh(version int64) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, r := range c.rules {
		if r.Version == version {
			return true
		}
	}
	return false
}
