export enum SoundId {
  // Abilities (mapped to slots 0-5)
  MissileLaunch = "missile-launch",
  RailgunFire = "railgun-fire",
  IonBurn = "ion-burn",
  PlasmaTorpedo = "plasma-torpedo",
  ShieldActivate = "shield-activate",
  Afterburner = "afterburner",

  // World events
  Thruster = "thruster",
  MiningLaser = "mining-laser",
  ExtractPulse = "extract-pulse",
  Explosion = "explosion",
  HitImpact = "hit-impact",
  LootPickup = "loot-pickup",

  // UI
  TargetLock = "target-lock",
  UIClick = "ui-click",

  // Music
  AmbientMusic = "ambient-music",
}

/** Maps AbilityType (from server item.go) to its sound */
export const ABILITY_TYPE_SOUNDS: Record<number, SoundId> = {
  // Weapon abilities
  1: SoundId.MissileLaunch,   // PulseLaser
  2: SoundId.MissileLaunch,   // PulseBarrage
  3: SoundId.RailgunFire,     // RailShot
  4: SoundId.RailgunFire,     // PiercingRound
  5: SoundId.IonBurn,         // IonBurn
  6: SoundId.IonBurn,         // IonOverload
  7: SoundId.PlasmaTorpedo,   // PlasmaBolt
  8: SoundId.PlasmaTorpedo,   // PlasmaTorpedo
  // Shield abilities
  20: SoundId.ShieldActivate, // EmergencyShield
  21: SoundId.ShieldActivate, // HardenedShield
  // Thruster abilities
  30: SoundId.Afterburner,    // Afterburner
  31: SoundId.Afterburner,    // MicroWarp
  // Mining — beam is looped by mining-laser.ts
  // 40: MiningBeam — no one-shot sound
  41: SoundId.ExtractPulse,   // ExtractPulse
};

export interface SoundDef {
  id: SoundId;
  src: string;
  volume: number;
  loop?: boolean;
}

export const SOUND_DEFS: SoundDef[] = [
  {
    id: SoundId.MissileLaunch,
    src: "/audio/sfx/missile-launch.ogg",
    volume: 0.5,
  },
  { id: SoundId.RailgunFire, src: "/audio/sfx/railgun-fire.ogg", volume: 0.6 },
  { id: SoundId.IonBurn, src: "/audio/sfx/ion-burn.ogg", volume: 0.5 },
  {
    id: SoundId.PlasmaTorpedo,
    src: "/audio/sfx/plasma-torpedo.ogg",
    volume: 0.7,
  },
  {
    id: SoundId.ShieldActivate,
    src: "/audio/sfx/shield-activate.ogg",
    volume: 0.6,
  },
  { id: SoundId.Afterburner, src: "/audio/sfx/afterburner.ogg", volume: 0.4 },
  {
    id: SoundId.Thruster,
    src: "/audio/sfx/thruster.ogg",
    volume: 0.05,
    loop: true,
  },
  {
    id: SoundId.MiningLaser,
    src: "/audio/sfx/mining-laser.ogg",
    volume: 0.12,
    loop: true,
  },
  { id: SoundId.ExtractPulse, src: "/audio/sfx/extract-pulse.ogg", volume: 0.5 },
  { id: SoundId.Explosion, src: "/audio/sfx/explosion.ogg", volume: 0.7 },
  { id: SoundId.HitImpact, src: "/audio/sfx/hit-impact.ogg", volume: 0.5 },
  { id: SoundId.LootPickup, src: "/audio/sfx/loot-pickup.ogg", volume: 0.6 },
  { id: SoundId.TargetLock, src: "/audio/sfx/target-lock.ogg", volume: 0.4 },
  { id: SoundId.UIClick, src: "/audio/sfx/ui-click.ogg", volume: 0.4 },
  {
    id: SoundId.AmbientMusic,
    src: "/audio/music/space-ambient.ogg",
    volume: 1.0,
    loop: true,
  },
];
