package hunter

import (
	"fmt"
	"time"

	"github.com/wowsims/wotlk/sim/core"
	"github.com/wowsims/wotlk/sim/core/proto"
)

const explosiveTrapSpellID = 49067

// How many Explosive Trap DoTs can tick at the same time (they stack on Warmane).
const maxExplosiveTrapStacks = 4

// True for the Explosive Trap itself and for any of its extra stacking DoT slots.
func (hunter *Hunter) isExplosiveTrap(spell *core.Spell) bool {
	return spell.ActionID.SpellID == explosiveTrapSpellID
}

func (hunter *Hunter) registerExplosiveTrapSpell(timer *core.Timer) {
	hasGlyph := hunter.HasMajorGlyph(proto.HunterMajorGlyph_GlyphOfExplosiveTrap)
	bonusPeriodicDamageMultiplier := .10 * float64(hunter.Talents.TrapMastery)

	// Builds the trap's DoT. Each stacking slot gets its own copy with a unique aura name.
	makeTrapDot := func(label string) core.DotConfig {
		return core.DotConfig{
			IsAOE: true,
			Aura: core.Aura{
				Label: label,
			},
			NumberOfTicks: 10,
			TickLength:    time.Second * 2,

			OnTick: func(sim *core.Simulation, target *core.Unit, dot *core.Dot) {
				baseDamage := 90 + 0.1*dot.Spell.RangedAttackPower(target)
				dot.Spell.DamageMultiplierAdditive += bonusPeriodicDamageMultiplier
				for _, aoeTarget := range sim.Encounter.TargetUnits {
					if hasGlyph {
						dot.Spell.CalcAndDealPeriodicDamage(sim, aoeTarget, baseDamage, dot.Spell.OutcomeRangedHitAndCritNoBlock)
					} else {
						dot.Spell.CalcAndDealPeriodicDamage(sim, aoeTarget, baseDamage, dot.Spell.OutcomeRangedHit)
					}
				}
				dot.Spell.DamageMultiplierAdditive -= bonusPeriodicDamageMultiplier
			},
		}
	}

	// Every spell that hosts a trap DoT: the trap itself plus the extra stacking slots.
	var trapDotSpells []*core.Spell

	// Called at the moment a trap goes off. Takes a free slot so traps stack instead of
	// refreshing each other. If every slot is busy, refreshes the one closest to expiring.
	applyTrapDot := func(sim *core.Simulation) {
		var chosen *core.Dot
		for _, slotSpell := range trapDotSpells {
			dot := slotSpell.AOEDot()
			if !dot.IsActive() {
				chosen = dot
				break
			}
			if chosen == nil || dot.RemainingDuration(sim) < chosen.RemainingDuration(sim) {
				chosen = dot
			}
		}
		chosen.Apply(sim)
	}

	hunter.ExplosiveTrap = hunter.RegisterSpell(core.SpellConfig{
		ActionID:    core.ActionID{SpellID: explosiveTrapSpellID},
		SpellSchool: core.SpellSchoolFire,
		ProcMask:    core.ProcMaskSpellDamage,
		Flags:       core.SpellFlagAPL,

		ManaCost: core.ManaCostOptions{
			BaseCost:   0.19,
			Multiplier: 1 - 0.2*float64(hunter.Talents.Resourcefulness),
		},
		Cast: core.CastConfig{
			DefaultCast: core.Cast{
				GCD: core.GCDDefault,
			},
			CD: core.Cooldown{
				Timer:    timer,
				Duration: time.Second*30 - time.Second*2*time.Duration(hunter.Talents.Resourcefulness),
			},
		},

		DamageMultiplierAdditive: 1 +
			.02*float64(hunter.Talents.TNT),
		CritMultiplier:   hunter.SpellCritMultiplier(1, 0),
		ThreatMultiplier: 1,

		Dot: makeTrapDot("Explosive Trap"),

		ApplyEffects: func(sim *core.Simulation, target *core.Unit, spell *core.Spell) {
			if sim.CurrentTime < 0 {
				// Traps only last 30s.
				if sim.CurrentTime < -time.Second*30 {
					return
				}

				// If using this on prepull, the trap effect will go off when the fight starts
				// instead of immediately.
				core.StartDelayedAction(sim, core.DelayedActionOptions{
					DoAt: 0,
					OnAction: func(sim *core.Simulation) {
						for _, aoeTarget := range sim.Encounter.TargetUnits {
							baseDamage := sim.Roll(523, 671) + 0.1*spell.RangedAttackPower(aoeTarget)
							baseDamage *= sim.Encounter.AOECapMultiplier()
							spell.CalcAndDealDamage(sim, aoeTarget, baseDamage, spell.OutcomeRangedHitAndCritNoBlock)
						}
						applyTrapDot(sim)
					},
				})
			} else {
				for _, aoeTarget := range sim.Encounter.TargetUnits {
					baseDamage := sim.Roll(523, 671) + 0.1*spell.RangedAttackPower(aoeTarget)
					baseDamage *= sim.Encounter.AOECapMultiplier()
					spell.CalcAndDealDamage(sim, aoeTarget, baseDamage, spell.OutcomeRangedHitAndCritNoBlock)
				}
				applyTrapDot(sim)
			}
		},
	})

	// Extra DoT slots so several traps can tick at once. They share the trap's ID, so their
	// damage is reported under "Explosive Trap". They are never cast, they only host a DoT.
	trapDotSpells = append(trapDotSpells, hunter.ExplosiveTrap)
	for i := 2; i <= maxExplosiveTrapStacks; i++ {
		trapDotSpells = append(trapDotSpells, hunter.RegisterSpell(core.SpellConfig{
			ActionID:    hunter.ExplosiveTrap.ActionID,
			SpellSchool: core.SpellSchoolFire,
			ProcMask:    core.ProcMaskSpellDamage,
			Flags:       core.SpellFlagNoOnCastComplete,

			DamageMultiplierAdditive: 1 +
				.02*float64(hunter.Talents.TNT),
			CritMultiplier:   hunter.SpellCritMultiplier(1, 0),
			ThreatMultiplier: 1,

			Dot: makeTrapDot(fmt.Sprintf("Explosive Trap (stack %d)", i)),
		}))
	}

	timeToTrapWeave := time.Millisecond * time.Duration(hunter.Options.TimeToTrapWeaveMs)
	halfWeaveTime := timeToTrapWeave / 2
	hunter.TrapWeaveSpell = hunter.RegisterSpell(core.SpellConfig{
		ActionID: hunter.ExplosiveTrap.ActionID.WithTag(1),
		Flags:    core.SpellFlagNoOnCastComplete | core.SpellFlagNoMetrics | core.SpellFlagNoLogs | core.SpellFlagAPL,

		ExtraCastCondition: func(sim *core.Simulation, target *core.Unit) bool {
			return hunter.ExplosiveTrap.CanCast(sim, target)
		},

		ApplyEffects: func(sim *core.Simulation, target *core.Unit, spell *core.Spell) {
			if sim.CurrentTime < 0 {
				hunter.mayMoveAt = sim.CurrentTime
			}

			// Assume we started running after the most recent ranged auto, so that time
			// can be subtracted from the run in.
			reachLocationAt := hunter.mayMoveAt + halfWeaveTime
			layTrapAt := max(reachLocationAt, sim.CurrentTime)
			doneAt := layTrapAt + halfWeaveTime

			hunter.AutoAttacks.DelayRangedUntil(sim, doneAt+time.Millisecond*500)

			if layTrapAt == sim.CurrentTime {
				hunter.ExplosiveTrap.Cast(sim, target)
				if doneAt > hunter.GCD.ReadyAt() {
					hunter.GCD.Set(doneAt)
				}
			} else {
				// Make sure the GCD doesn't get used while we're waiting.
				hunter.WaitUntil(sim, doneAt)

				core.StartDelayedAction(sim, core.DelayedActionOptions{
					DoAt: layTrapAt,
					OnAction: func(sim *core.Simulation) {
						hunter.GCD.Reset()
						hunter.ExplosiveTrap.Cast(sim, target)
						if doneAt > hunter.GCD.ReadyAt() {
							hunter.GCD.Set(doneAt)
						}
					},
				})
			}
		},
	})
}
